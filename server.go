package main

// Local web UI: a tiny HTTP server on 127.0.0.1 that the app opens in the
// default browser. Native file pickers are shown through osascript.

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed ui.html
var uiHTML []byte

const defaultPort = 47319
const helloToken = "stardict2mac-hello"

type server struct {
	mu       sync.Mutex
	job      *Job
	lastSeen time.Time
	token    string

	// currently inspected dictionary (kept open for previews)
	cur     *StarDict
	curPath string
	curConv *Converter
	curTmp  string
}

func runServer() {
	s := &server{lastSeen: time.Now(), token: strconv.FormatInt(rand.Int63(), 36)}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", defaultPort))
	if err != nil {
		// Already running? Then just bring its window up.
		if r, e := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/hello", defaultPort)); e == nil {
			buf := make([]byte, 64)
			n, _ := r.Body.Read(buf)
			r.Body.Close()
			if strings.HasPrefix(string(buf[:n]), helloToken) {
				openURL(fmt.Sprintf("http://127.0.0.1:%d/", defaultPort))
				return
			}
		}
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal(err)
		}
	}
	url := fmt.Sprintf("http://%s/", ln.Addr().String())

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(helloToken)) })
	mux.HandleFunc("/api/state", s.api(s.handleState))
	mux.HandleFunc("/api/pick", s.api(s.handlePick))
	mux.HandleFunc("/api/inspect", s.api(s.handleInspect))
	mux.HandleFunc("/api/preview", s.api(s.handlePreview))
	mux.HandleFunc("/api/build", s.api(s.handleBuild))
	mux.HandleFunc("/api/cancel", s.api(s.handleCancel))
	mux.HandleFunc("/api/job", s.api(s.handleJob))
	mux.HandleFunc("/api/open", s.api(s.handleOpen))
	mux.HandleFunc("/api/installed", s.api(s.handleInstalled))
	mux.HandleFunc("/api/quit", s.api(s.handleQuit))
	mux.HandleFunc("/api/kit/download", s.api(s.handleKitDownload))
	mux.HandleFunc("/api/kit/choose", s.api(s.handleKitChoose))

	srv := &http.Server{Handler: mux}
	go s.watchdog(srv)
	fmt.Fprintln(os.Stderr, "StarDict2Mac running at", url)
	if os.Getenv("STARDICT2MAC_NO_BROWSER") == "" {
		go func() {
			time.Sleep(200 * time.Millisecond)
			openURL(url)
		}()
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
	s.cleanupPreview()
}

// Quit when the page has been gone for a while and nothing is running.
func (s *server) watchdog(srv *http.Server) {
	idle := 15 * time.Minute
	if v, err := time.ParseDuration(os.Getenv("STARDICT2MAC_IDLE")); err == nil {
		idle = v
	}
	for {
		time.Sleep(20 * time.Second)
		s.mu.Lock()
		running := s.job != nil && s.job.Snapshot(1<<30).State == "running"
		quiet := time.Since(s.lastSeen) > idle
		s.mu.Unlock()
		if quiet && !running {
			srv.Shutdown(context.Background())
			return
		}
	}
}

func openURL(u string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("/usr/bin/open", u).Start()
	case "linux":
		exec.Command("xdg-open", u).Start()
	}
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.touch()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(uiHTML)
}

func (s *server) touch() {
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.mu.Unlock()
}

type apiFunc func(r *http.Request, body map[string]any) (any, error)

func (s *server) api(f apiFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only accept requests from our own page (basic CSRF protection).
		if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "http://127.0.0.1:") && !strings.HasPrefix(o, "http://localhost:") {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Method == http.MethodPost && r.Header.Get("X-SD2M") != "1" {
			http.Error(w, "forbidden", 403)
			return
		}
		s.touch()
		body := map[string]any{}
		if r.Method == http.MethodPost {
			json.NewDecoder(r.Body).Decode(&body)
		}
		res, err := f(r, body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(res)
	}
}

func str(b map[string]any, k string) string {
	if v, ok := b[k].(string); ok {
		return v
	}
	return ""
}

func boolv(b map[string]any, k string, def bool) bool {
	if v, ok := b[k].(bool); ok {
		return v
	}
	return def
}

func intv(b map[string]any, k string, def int) int {
	if v, ok := b[k].(float64); ok {
		return int(v)
	}
	return def
}

// ------------------------------------------------------------------ handlers

func (s *server) handleState(r *http.Request, _ map[string]any) (any, error) {
	st := map[string]any{"version": AppVersion, "os": runtime.GOOS}
	st["kit"] = FindKit()
	st["kitMirror"] = kitMirrorPage
	st["kitApple"] = kitApplePage
	if runtime.GOOS == "darwin" {
		if err := CheckSystemTools(); err != nil {
			st["toolsError"] = err.Error()
		}
	}
	s.mu.Lock()
	if s.job != nil {
		snap := s.job.Snapshot(1 << 30)
		st["job"] = snap
	}
	s.mu.Unlock()
	st["dictionariesDir"] = dictionariesDir()
	return st, nil
}

func osascript(lines ...string) (string, error) {
	var args []string
	for _, l := range lines {
		args = append(args, "-e", l)
	}
	out, err := exec.Command("/usr/bin/osascript", args...).CombinedOutput()
	o := strings.TrimSpace(string(out))
	if err != nil {
		if strings.Contains(o, "-128") { // user cancelled
			return "", nil
		}
		return "", fmt.Errorf("%s", o)
	}
	return o, nil
}

func (s *server) handlePick(r *http.Request, b map[string]any) (any, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("file picker is only available on macOS; paste a path instead")
	}
	var p string
	var err error
	switch str(b, "kind") {
	case "folder":
		p, err = osascript(`tell current application to activate`,
			`POSIX path of (choose folder with prompt "Choose a folder that contains a StarDict dictionary (.ifo, .idx, .dict)")`)
	default:
		p, err = osascript(`tell current application to activate`,
			`POSIX path of (choose file with prompt "Choose a StarDict .ifo file (or a .tar.gz / .tar.bz2 / .zip archive of one)")`)
	}
	if err != nil {
		return nil, err
	}
	if p == "" {
		return map[string]any{"cancelled": true}, nil
	}
	return map[string]any{"path": p}, nil
}

func (s *server) cleanupPreview() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		s.cur.Close()
		s.cur = nil
	}
	if s.curTmp != "" {
		os.RemoveAll(s.curTmp)
		s.curTmp = ""
	}
}

func (s *server) handleInspect(r *http.Request, b map[string]any) (any, error) {
	path := str(b, "path")
	if path == "" {
		return nil, errors.New("no path given")
	}
	ifo, err := ResolveSource(path, filepath.Join(cacheDir(), "extract"))
	if err != nil {
		return nil, err
	}
	s.cleanupPreview()
	tmp := filepath.Join(cacheDir(), "preview")
	os.RemoveAll(tmp)
	os.MkdirAll(tmp, 0755)
	sd, err := OpenStarDict(ifo, tmp, nil)
	if err != nil {
		return nil, err
	}
	opts := DefaultOptions(sd.Info)
	opts.Lang = DetectLang(sd)
	s.mu.Lock()
	s.cur, s.curPath, s.curTmp = sd, ifo, tmp
	s.curConv = NewConverter(sd, opts)
	s.mu.Unlock()

	types := sd.Info.SameTypeSequence
	if types == "" && len(sd.Entries) > 0 {
		if d, err := sd.RawData(0); err == nil {
			for _, f := range sd.Fields(d) {
				types += string(f.Type)
			}
		}
	}
	dest := filepath.Join(dictionariesDir(), opts.FileName+".dictionary")
	var samples []string
	for _, i := range []int{0, len(sd.Entries) / 3, len(sd.Entries) * 2 / 3} {
		if i < len(sd.Entries) && (len(samples) == 0 || samples[len(samples)-1] != sd.Entries[i].Word) {
			samples = append(samples, sd.Entries[i].Word)
		}
	}
	return map[string]any{
		"ifo":         ifo,
		"source":      path,
		"bookname":    sd.Info.BookName,
		"author":      sd.Info.Author,
		"description": sd.Info.Description,
		"version":     sd.Info.Version,
		"date":        sd.Info.Date,
		"entries":     len(sd.Entries),
		"synonyms":    len(sd.Syns),
		"types":       types,
		"hasRes":      sd.ResDir != "",
		"defaults":    opts,
		"installed":   dirExists(dest),
		"samples":     samples,
		"lang":        opts.Lang,
	}, nil
}

func optsFromBody(b map[string]any, base Options) Options {
	o := base
	if v := strings.TrimSpace(str(b, "name")); v != "" {
		o.Name = v
	}
	if v := strings.TrimSpace(str(b, "fileName")); v != "" {
		o.FileName = SafeFileName(v, o.Name)
	}
	if v := str(b, "font"); v != "" {
		o.Font = v
	}
	o.FontSize = intv(b, "fontSize", o.FontSize)
	o.ShowHeadword = boolv(b, "showHeadword", o.ShowHeadword)
	o.LinkXrefs = boolv(b, "linkXrefs", o.LinkXrefs)
	o.UseSynonyms = boolv(b, "useSynonyms", o.UseSynonyms)
	o.Install = boolv(b, "install", o.Install)
	o.BundleID = BundleIDFor(o.Name + "|" + o.FileName)
	return o
}

// handlePreview renders one entry (by headword, or a sample) as an HTML page
// using the same CSS the dictionary will use.
func (s *server) handlePreview(r *http.Request, b map[string]any) (any, error) {
	s.mu.Lock()
	sd, conv := s.cur, s.curConv
	s.mu.Unlock()
	if sd == nil {
		return nil, errors.New("no dictionary loaded")
	}
	opts := optsFromBody(b, conv.opts)
	c := &Converter{opts: opts, lookup: conv.lookup, hasRes: conv.hasRes}
	word := strings.TrimSpace(str(b, "word"))
	idx := -1
	var matches []string
	if word == "" {
		idx = 0
	} else if i, ok := conv.lookup[word]; ok {
		idx = i
	} else {
		// linear search (exact first, then prefix)
		for i, e := range sd.Entries {
			if e.Word == word {
				idx = i
				break
			}
		}
		if idx < 0 {
			for i, e := range sd.Entries {
				if strings.HasPrefix(e.Word, word) {
					if idx < 0 {
						idx = i
					}
					matches = append(matches, e.Word)
					if len(matches) >= 12 {
						break
					}
				}
			}
		}
	}
	if idx < 0 {
		return map[string]any{"found": false}, nil
	}
	xml, err := c.EntryXML(sd, idx, nil)
	if err != nil {
		return nil, err
	}
	// strip the d:entry wrapper and d:index elements for browser display
	body := xml
	if i := strings.Index(body, ">"); i >= 0 {
		body = body[i+1:]
	}
	body = strings.TrimSuffix(strings.TrimSpace(body), "</d:entry>")
	for {
		i := strings.Index(body, "<d:index ")
		if i < 0 {
			break
		}
		j := strings.Index(body[i:], "/>")
		if j < 0 {
			break
		}
		body = body[:i] + body[i+j+2:]
	}
	css := BuildCSS(opts)
	css = strings.ReplaceAll(css, "d|entry", ".entry")
	css = strings.Replace(css, "@namespace d url(http://www.apple.com/DTDs/DictionaryService-1.0.rng);", "", 1)
	page := `<!DOCTYPE html><html><head><meta charset="utf-8"><style>
body{margin:0;padding:18px 22px;background:#fff;color:#1d1d1f}
@media (prefers-color-scheme: dark){body{background:#1e1e1e;color:#e8e8e8}}
a{color:#0a66d8}
` + css + `</style></head><body><div class="entry">` + body + `</div>
<script>document.addEventListener('click',e=>{const a=e.target.closest('a');if(!a)return;e.preventDefault();const h=a.getAttribute('href')||'';parent.postMessage({sd2mLink:h,text:a.textContent},'*')});</script>
</body></html>`
	id := ""
	if strings.HasPrefix(xml, `<d:entry id="`) {
		id = strings.SplitN(xml[13:], `"`, 2)[0]
	}
	return map[string]any{"found": true, "word": sd.Entries[idx].Word, "id": id, "html": page, "matches": matches}, nil
}

// previewByID lets links inside the preview navigate.
func (s *server) entryWordByID(id string) string {
	s.mu.Lock()
	sd := s.cur
	s.mu.Unlock()
	if sd == nil || !strings.HasPrefix(id, "e") {
		return ""
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n < 0 || n >= len(sd.Entries) {
		return ""
	}
	return sd.Entries[n].Word
}

func (s *server) handleBuild(r *http.Request, b map[string]any) (any, error) {
	s.mu.Lock()
	if s.job != nil && s.job.Snapshot(1<<30).State == "running" {
		s.mu.Unlock()
		return nil, errors.New("a conversion is already running")
	}
	path := s.curPath
	base := Options{}
	if s.curConv != nil {
		base = s.curConv.opts
	}
	s.mu.Unlock()
	if p := str(b, "path"); p != "" {
		path = p
	}
	if path == "" {
		return nil, errors.New("choose a dictionary first")
	}
	if base.Name == "" {
		info, err := ParseIfo(path)
		if err != nil {
			return nil, err
		}
		base = DefaultOptions(info)
	}
	opts := optsFromBody(b, base)
	j := &Job{ID: strconv.FormatInt(time.Now().UnixNano(), 36), Source: path, Opts: opts}
	s.mu.Lock()
	s.job = j
	s.mu.Unlock()
	go func() {
		j.Run(context.Background())
		snap := j.Snapshot(1 << 30)
		if runtime.GOOS == "darwin" {
			msg := "Finished: " + opts.Name
			if snap.State != "done" {
				msg = "Conversion " + snap.State + ": " + opts.Name
			}
			osascript(fmt.Sprintf(`display notification %q with title "StarDict2Mac"`, msg))
		}
	}()
	return map[string]any{"id": j.ID}, nil
}

func (s *server) handleCancel(r *http.Request, b map[string]any) (any, error) {
	s.mu.Lock()
	j := s.job
	s.mu.Unlock()
	if j != nil {
		j.Cancel()
	}
	return map[string]any{"ok": true}, nil
}

func (s *server) handleJob(r *http.Request, b map[string]any) (any, error) {
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	s.mu.Lock()
	j := s.job
	s.mu.Unlock()
	if j == nil {
		return map[string]any{"none": true}, nil
	}
	return j.Snapshot(since), nil
}

func (s *server) handleOpen(r *http.Request, b map[string]any) (any, error) {
	if str(b, "what") == "entry" {
		return map[string]any{"word": s.entryWordByID(str(b, "id"))}, nil
	}
	if runtime.GOOS != "darwin" {
		return nil, errors.New("only on macOS")
	}
	switch str(b, "what") {
	case "dictionary":
		// Restart Dictionary.app so it notices the new dictionary.
		osascript(`if application id "com.apple.Dictionary" is running then tell application id "com.apple.Dictionary" to quit`)
		time.Sleep(700 * time.Millisecond)
		return map[string]any{"ok": true}, exec.Command("/usr/bin/open", "-b", "com.apple.Dictionary").Run()
	case "reveal":
		p := str(b, "path")
		if p == "" {
			p = dictionariesDir()
		}
		return map[string]any{"ok": true}, exec.Command("/usr/bin/open", "-R", p).Run()
	case "folder":
		os.MkdirAll(dictionariesDir(), 0755)
		return map[string]any{"ok": true}, exec.Command("/usr/bin/open", dictionariesDir()).Run()
	case "lookup":
		w := str(b, "word")
		return map[string]any{"ok": true}, exec.Command("/usr/bin/open", "dict://"+w).Run()
	case "entry":
		return map[string]any{"word": s.entryWordByID(str(b, "id"))}, nil
	}
	return nil, errors.New("unknown action")
}

func (s *server) handleInstalled(r *http.Request, b map[string]any) (any, error) {
	ents, _ := os.ReadDir(dictionariesDir())
	var out []map[string]any
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".dictionary") {
			info, _ := e.Info()
			m := map[string]any{"file": e.Name(), "path": filepath.Join(dictionariesDir(), e.Name())}
			if info != nil {
				m["modified"] = info.ModTime()
			}
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *server) handleQuit(r *http.Request, b map[string]any) (any, error) {
	s.mu.Lock()
	j := s.job
	s.mu.Unlock()
	if j != nil {
		j.Cancel()
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		s.cleanupPreview()
		os.Exit(0)
	}()
	return map[string]any{"ok": true}, nil
}

func (s *server) handleKitDownload(r *http.Request, b map[string]any) (any, error) {
	if _, err := DownloadKit(nil); err != nil {
		return nil, err
	}
	return FindKit(), nil
}

// handleKitChoose lets the user point at a kit installed from Apple.
func (s *server) handleKitChoose(r *http.Request, b map[string]any) (any, error) {
	p := str(b, "path")
	if p == "" {
		if runtime.GOOS != "darwin" {
			return nil, errors.New("folder picker is only available on macOS")
		}
		var err error
		p, err = osascript(`tell current application to activate`,
			`POSIX path of (choose folder with prompt "Choose the “Dictionary Development Kit” folder (from Additional Tools for Xcode)")`)
		if err != nil {
			return nil, err
		}
		if p == "" {
			return map[string]any{"cancelled": true}, nil
		}
	}
	if _, err := SetKitPath(p); err != nil {
		return nil, err
	}
	return FindKit(), nil
}
