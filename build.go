package main

// Build pipeline: StarDict -> DDK sources -> Apple's build_dict.sh -> install.

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const AppVersion = "1.2.0"

// ------------------------------------------------------------------ paths

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func appSupportDir() string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(homeDir(), "Library", "Application Support", "StarDict2Mac")
	}
	return filepath.Join(homeDir(), ".local", "share", "stardict2mac")
}

func cacheDir() string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(homeDir(), "Library", "Caches", "StarDict2Mac")
	}
	return filepath.Join(homeDir(), ".cache", "stardict2mac")
}

func dictionariesDir() string {
	return filepath.Join(homeDir(), "Library", "Dictionaries")
}

// CheckSystemTools verifies the base macOS tools build_dict.sh relies on.
func CheckSystemTools() error {
	var missing []string
	for _, t := range []string{"perl", "xmllint", "xsltproc", "plutil", "sed", "tr"} {
		if _, err := exec.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing system tools: %s", strings.Join(missing, ", "))
	}
	return nil
}

// ------------------------------------------------------------------ source resolution

var archiveExts = []string{".tar.gz", ".tgz", ".tar.bz2", ".tbz", ".tbz2", ".tar.xz", ".txz", ".tar", ".zip", ".7z"}

func isArchive(p string) bool {
	lp := strings.ToLower(p)
	for _, e := range archiveExts {
		if strings.HasSuffix(lp, e) {
			return true
		}
	}
	return false
}

// ResolveSource turns a user-chosen path (ifo, any dictionary file, folder or
// archive) into an .ifo path. Archives are extracted into extractRoot.
func ResolveSource(p, extractRoot string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(homeDir(), p[2:])
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return FindIfo(p)
	}
	if strings.EqualFold(filepath.Ext(p), ".ifo") || isBGL(p) {
		return p, nil
	}
	if isArchive(p) {
		h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", p, st.Size(), st.ModTime().UnixNano())))
		dst := filepath.Join(extractRoot, "src-"+hex.EncodeToString(h[:6]))
		if ifo, err := FindIfo(dst); err == nil {
			return ifo, nil
		}
		os.RemoveAll(dst)
		if err := os.MkdirAll(dst, 0755); err != nil {
			return "", err
		}
		tarBin := "tar"
		if fileExists("/usr/bin/tar") {
			tarBin = "/usr/bin/tar"
		}
		out, err := exec.Command(tarBin, "-xf", p, "-C", dst).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("could not extract archive: %v %s", err, strings.TrimSpace(string(out)))
		}
		return FindIfo(dst)
	}
	// Some other file of the dictionary (.idx, .dict.dz, ...): look beside it.
	return FindIfo(filepath.Dir(p))
}

// ------------------------------------------------------------------ job

type Job struct {
	mu       sync.Mutex
	ID       string    `json:"id"`
	Source   string    `json:"source"`
	Opts     Options   `json:"opts"`
	State    string    `json:"state"` // running, done, error, cancelled
	Phase    string    `json:"phase"`
	Percent  float64   `json:"percent"`
	Log      []string  `json:"-"`
	LogBase  int       `json:"-"` // number of lines dropped from the front
	Result   string    `json:"result"`
	Error    string    `json:"error"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	cancel   context.CancelFunc
	repeats  map[string]int
}

type JobSnapshot struct {
	ID        string   `json:"id"`
	Source    string   `json:"source"`
	Name      string   `json:"name"`
	State     string   `json:"state"`
	Phase     string   `json:"phase"`
	Percent   float64  `json:"percent"`
	Result    string   `json:"result"`
	Installed bool     `json:"installed"`
	Error     string   `json:"error"`
	Elapsed   float64  `json:"elapsed"`
	Log       []string `json:"log"`
	LogNext   int      `json:"logNext"`
}

func (j *Job) Snapshot(since int) JobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	end := time.Now()
	if !j.Finished.IsZero() {
		end = j.Finished
	}
	s := JobSnapshot{ID: j.ID, Source: j.Source, Name: j.Opts.Name, State: j.State, Phase: j.Phase,
		Percent: j.Percent, Result: j.Result, Installed: j.Opts.Install, Error: j.Error,
		Elapsed: end.Sub(j.Started).Seconds()}
	start := since - j.LogBase
	if start < 0 {
		start = 0
	}
	if start < len(j.Log) {
		s.Log = append([]string(nil), j.Log[start:]...)
	}
	s.LogNext = j.LogBase + len(j.Log)
	return s
}

func (j *Job) logf(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	j.mu.Lock()
	j.Log = append(j.Log, time.Now().Format("15:04:05 ")+line)
	if len(j.Log) > 3000 {
		drop := len(j.Log) - 2500
		j.Log = append([]string(nil), j.Log[drop:]...)
		j.LogBase += drop
	}
	j.mu.Unlock()
	if logToStderr {
		fmt.Fprintln(os.Stderr, line)
	}
}

var logToStderr bool

func (j *Job) set(phase string, pct float64) {
	j.mu.Lock()
	if phase != "" {
		j.Phase = phase
	}
	if pct >= 0 {
		j.Percent = pct
	}
	j.mu.Unlock()
}

func (j *Job) Cancel() {
	j.mu.Lock()
	c := j.cancel
	j.mu.Unlock()
	if c != nil {
		c()
	}
}

// DDK progress markers (message prefix -> overall percent).
var ddkStages = []struct {
	prefix string
	pct    float64
	label  string
}{
	{"- Building", 36, "Apple DDK: starting"},
	{"- Checking source", 37, "Apple DDK: validating XML"},
	{"- Cleaning objects", 41, "Apple DDK: preparing"},
	{"- Preparing dictionary template", 41, "Apple DDK: preparing"},
	{"- Preprocessing dictionary sources", 42, "Apple DDK: preprocessing entries"},
	{"- Extracting index data", 56, "Apple DDK: extracting index"},
	{"- Preparing dictionary bundle", 63, "Apple DDK: creating bundle"},
	{"- Adding body data", 64, "Apple DDK: compressing entries"},
	{"- Preparing index data", 80, "Apple DDK: preparing search keys"},
	{"- Building key_text index", 88, "Apple DDK: building search index"},
	{"- Building reference index", 95, "Apple DDK: building link index"},
	{"- Fixing dictionary property", 97, "Apple DDK: finishing"},
	{"- Copying", 98, "Apple DDK: copying styles"},
	{"- Finished", 99, "Apple DDK: done"},
}

// Run executes the whole conversion. It is safe to call in a goroutine.
func (j *Job) Run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	j.mu.Lock()
	j.cancel = cancel
	j.State = "running"
	j.Started = time.Now()
	j.repeats = map[string]int{}
	j.mu.Unlock()
	defer cancel()

	res, err := j.run(ctx)
	j.mu.Lock()
	j.Finished = time.Now()
	if err != nil {
		if ctx.Err() != nil {
			j.State = "cancelled"
			j.Error = "Cancelled"
		} else {
			j.State = "error"
			j.Error = err.Error()
		}
	} else {
		j.State = "done"
		j.Result = res
		j.Percent = 100
		j.Phase = "Finished"
	}
	j.mu.Unlock()
	if err != nil {
		j.logf("ERROR: %v", err)
	} else {
		j.logf("Finished in %s: %s", time.Since(j.Started).Round(time.Second), res)
	}
}

func (j *Job) run(ctx context.Context) (string, error) {
	opts := j.Opts
	if opts.FileName == "" {
		opts.FileName = SafeFileName(opts.Name, "Dictionary")
	}
	opts.FileName = SafeFileName(opts.FileName, opts.Name)
	if opts.BundleID == "" {
		opts.BundleID = BundleIDFor(opts.Name + "|" + opts.FileName)
	}

	j.set("Checking tools", 0)
	if runtime.GOOS == "darwin" {
		if err := CheckSystemTools(); err != nil {
			return "", err
		}
	}
	ddk, err := EnsureDDK()
	if err != nil {
		return "", err
	}
	j.logf("Using Dictionary Development Kit at %s", ddk)

	work := filepath.Join(cacheDir(), "build", opts.FileName)
	os.RemoveAll(work)
	if err := os.MkdirAll(work, 0755); err != nil {
		return "", err
	}
	j.logf("Work folder: %s", work)

	j.set("Opening StarDict files", 1)
	ifo, err := ResolveSource(j.Source, filepath.Join(cacheDir(), "extract"))
	if err != nil {
		return "", err
	}
	j.logf("Source: %s", ifo)
	sd, err := OpenStarDict(ifo, work, func(s string) { j.logf("%s", s); j.set(s, -1) })
	if err != nil {
		return "", err
	}
	j.logf("%s — %d entries, %d synonyms, type %q", sd.Info.BookName, len(sd.Entries), len(sd.Syns), sd.Info.SameTypeSequence)
	if opts.Lang == "" {
		opts.Lang = DetectLang(sd)
	}

	j.set("Converting entries", 2)
	t0 := time.Now()
	err = WriteSources(ctx, sd, opts, work, func(done, total int) {
		j.set(fmt.Sprintf("Converting entries (%d / %d)", done, total), 2+33*float64(done)/float64(max(total, 1)))
	}, j.logf)
	sd.Close()
	if err != nil {
		return "", err
	}
	if st, e := os.Stat(filepath.Join(work, "dict.xml")); e == nil {
		j.logf("Wrote dict.xml (%.1f MB) in %s", float64(st.Size())/1e6, time.Since(t0).Round(time.Second))
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	// ---- Apple's build script
	j.set("Apple DDK: starting", 35)
	cmd := exec.CommandContext(ctx, "/bin/sh", filepath.Join(ddk, "bin", "build_dict.sh"),
		"-v", "10.11", opts.FileName, "dict.xml", "dict.css", "dict.plist")
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin:"+os.Getenv("PATH"),
		"LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8",
		"DICT_DEV_KIT_OBJ_DIR=objects",
	)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("could not start build_dict.sh: %w", err)
	}
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			j.ddkLine(sc.Text())
		}
	}()
	go func() {
		<-ctx.Done()
		killProcessGroup(cmd)
	}()
	werr := cmd.Wait()
	pw.Close()
	<-scanDone
	j.flushRepeats()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if werr != nil {
		return "", fmt.Errorf("Apple's build_dict.sh failed (%v) — see the log for details", werr)
	}
	built := filepath.Join(work, "objects", opts.FileName+".dictionary")
	if !dirExists(built) {
		return "", errors.New("build finished but no .dictionary bundle was produced")
	}

	// ---- install / save
	var dest string
	if opts.Install {
		dest = filepath.Join(dictionariesDir(), opts.FileName+".dictionary")
	} else {
		outDir := filepath.Dir(filepath.Dir(ifo))
		if strings.HasPrefix(ifo, filepath.Join(cacheDir(), "extract")) {
			outDir = filepath.Dir(j.Source)
		}
		dest = filepath.Join(outDir, opts.FileName+".dictionary")
	}
	j.set("Installing", 99.5)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", err
	}
	if dirExists(dest) {
		j.logf("Replacing existing %s", dest)
		if err := os.RemoveAll(dest); err != nil {
			return "", fmt.Errorf("could not remove old %s: %w", dest, err)
		}
	}
	if err := os.Rename(built, dest); err != nil {
		if err := copyTree(built, dest); err != nil {
			return "", fmt.Errorf("could not copy dictionary to %s: %w", dest, err)
		}
	}
	if opts.Install {
		now := time.Now()
		os.Chtimes(dictionariesDir(), now, now)
	}
	os.RemoveAll(work)
	return dest, nil
}

// ddkLine handles one line of build_dict.sh output.
func (j *Job) ddkLine(line string) {
	t := strings.TrimSpace(line)
	if t == "" {
		return
	}
	if strings.HasPrefix(t, "- ") {
		for _, st := range ddkStages {
			if strings.HasPrefix(t, st.prefix) {
				j.set(st.label, st.pct)
				break
			}
		}
		j.logf("%s", t)
		return
	}
	// Collapse very repetitive warnings.
	key := ""
	switch {
	case strings.Contains(t, "Duplicate index. Skipped"):
		key = "duplicate index keys skipped"
	case strings.Contains(t, "No title for entry"):
		key = "entries without title"
	case strings.Contains(t, "Maybe more"):
		return
	}
	if key != "" {
		j.mu.Lock()
		j.repeats[key]++
		n := j.repeats[key]
		j.mu.Unlock()
		if n <= 3 {
			j.logf("  %s", t)
		} else if n == 4 {
			j.logf("  (further %s messages are counted, not shown)", key)
		}
		return
	}
	j.logf("  %s", t)
}

func (j *Job) flushRepeats() {
	j.mu.Lock()
	r := j.repeats
	j.mu.Unlock()
	for k, n := range r {
		if n > 3 {
			j.logf("  %d × %s", n, k)
		}
	}
}
