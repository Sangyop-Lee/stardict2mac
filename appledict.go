package main

// Writes Dictionary Development Kit sources (XML, CSS, Info.plist).

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const frontMatterID = "front_back_matter"

func entryID(i int) string { return fmt.Sprintf("e%d", i) }

var nonASCIIName = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

// SafeFileName produces an ASCII bundle name; falls back to a hash.
func SafeFileName(s string, fallback string) string {
	t := strings.TrimSpace(nonASCIIName.ReplaceAllString(s, ""))
	t = strings.Trim(t, ". ")
	if len(t) < 2 {
		t = strings.TrimSpace(nonASCIIName.ReplaceAllString(fallback, ""))
	}
	if len(t) < 2 {
		h := sha1.Sum([]byte(s))
		t = "Dictionary-" + hex.EncodeToString(h[:4])
	}
	if len(t) > 80 {
		t = t[:80]
	}
	return t
}

func BundleIDFor(name string) string {
	h := sha1.Sum([]byte(name))
	slug := strings.ToLower(regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(name, ""))
	if len(slug) > 30 {
		slug = slug[:30]
	}
	if slug == "" {
		slug = "dict"
	}
	return "local.stardict2mac." + slug + "." + hex.EncodeToString(h[:4])
}

func DefaultOptions(info IfoInfo) Options {
	name := info.BookName
	return Options{
		Name:         name,
		FileName:     SafeFileName(filepath.Base(info.Base), name),
		BundleID:     BundleIDFor(name + "|" + filepath.Base(info.Base)),
		Font:         "default",
		FontSize:     100,
		ShowHeadword: true,
		LinkXrefs:    true,
		UseSynonyms:  true,
		Install:      true,
	}
}

func NewConverter(sd *StarDict, opts Options) *Converter {
	c := &Converter{opts: opts, hasRes: sd.ResDir != ""}
	if opts.LinkXrefs {
		c.lookup = make(map[string]int, len(sd.Entries)+len(sd.Syns))
		for i, e := range sd.Entries {
			if _, ok := c.lookup[e.Word]; !ok {
				c.lookup[e.Word] = i
			}
		}
		for _, s := range sd.Syns {
			if _, ok := c.lookup[s.Word]; !ok {
				c.lookup[s.Word] = int(s.Index)
			}
		}
	}
	return c
}

// EntryXML renders one <d:entry>. Returns "" if the entry should be skipped.
func (c *Converter) EntryXML(sd *StarDict, i int, syns []string) (string, error) {
	e := sd.Entries[i]
	word := keyText(e.Word)
	if word == "" {
		return "", nil
	}
	data, err := sd.RawData(i)
	if err != nil {
		return "", err
	}
	body := c.FieldsToXHTML(sd.Fields(data))
	var b strings.Builder
	b.Grow(len(body) + 256)
	ta := escAttr(word)
	b.WriteString(`<d:entry id="`)
	b.WriteString(entryID(i))
	b.WriteString(`" d:title="`)
	b.WriteString(ta)
	b.WriteString(`">`)
	b.WriteString(`<d:index d:value="` + ta + `" d:title="` + ta + `"/>`)
	for _, s := range syns {
		s = keyText(s)
		if s == "" || s == word {
			continue
		}
		sa := escAttr(s)
		b.WriteString(`<d:index d:value="` + sa + `" d:title="` + sa + `"/>`)
	}
	if c.opts.ShowHeadword {
		b.WriteString(`<h1 class="hw">`)
		escText(&b, word)
		b.WriteString(`</h1>`)
	}
	b.WriteString(`<div class="sd">`)
	b.WriteString(body)
	b.WriteString("</div></d:entry>\n")
	return b.String(), nil
}

type ProgressFunc func(done, total int)

// WriteSources writes dict.xml, dict.css, dict.plist (and OtherResources) into dir.
func WriteSources(ctx context.Context, sd *StarDict, opts Options, dir string, prog ProgressFunc, logf func(string, ...any)) error {
	c := NewConverter(sd, opts)
	n := len(sd.Entries)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	synMap := map[int][]string{}
	if opts.UseSynonyms {
		for _, s := range sd.Syns {
			synMap[int(s.Index)] = append(synMap[int(s.Index)], s.Word)
		}
	}
	for k, v := range synMap {
		synMap[k] = dedupe(v)
	}

	f, err := os.Create(filepath.Join(dir, "dict.xml"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 4<<20)
	io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n")
	io.WriteString(w, `<d:dictionary xmlns="http://www.w3.org/1999/xhtml" xmlns:d="http://www.apple.com/DTDs/DictionaryService-1.0.rng">`+"\n")

	// Front/back matter
	io.WriteString(w, c.frontMatter(sd))

	// Parallel conversion in ordered chunks.
	const chunk = 2000
	type result struct {
		idx int
		s   string
		err error
	}
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	jobs := make(chan int)
	results := make(chan result, workers*2)
	go func() {
		defer close(jobs)
		for start := 0; start < n; start += chunk {
			select {
			case jobs <- start:
			case <-ctx.Done():
				return
			}
		}
	}()
	done := make(chan struct{})
	for k := 0; k < workers; k++ {
		go func() {
			for start := range jobs {
				end := start + chunk
				if end > n {
					end = n
				}
				var sb strings.Builder
				var ferr error
				for i := start; i < end; i++ {
					s, err := c.EntryXML(sd, i, synMap[i])
					if err != nil {
						ferr = fmt.Errorf("entry %d (%q): %w", i, sd.Entries[i].Word, err)
						break
					}
					sb.WriteString(s)
				}
				select {
				case results <- result{start, sb.String(), ferr}:
				case <-done:
					return
				}
			}
		}()
	}
	pending := map[int]result{}
	next := 0
	lastReport := time.Now()
	var firstErr error
	for next < n && firstErr == nil {
		select {
		case <-ctx.Done():
			close(done)
			return ctx.Err()
		case r := <-results:
			pending[r.idx] = r
		}
		for {
			r, ok := pending[next]
			if !ok {
				break
			}
			delete(pending, next)
			if r.err != nil {
				firstErr = r.err
				break
			}
			if _, err := io.WriteString(w, r.s); err != nil {
				firstErr = err
				break
			}
			next += chunk
			if next > n {
				next = n
			}
			if prog != nil && (time.Since(lastReport) > 200*time.Millisecond || next >= n) {
				prog(next, n)
				lastReport = time.Now()
			}
		}
	}
	close(done)
	if firstErr != nil {
		return firstErr
	}
	io.WriteString(w, "</d:dictionary>\n")
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(dir, "dict.css"), []byte(BuildCSS(opts)), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "dict.plist"), []byte(BuildPlist(sd.Info, opts)), 0644); err != nil {
		return err
	}
	if sd.ResDir != "" {
		logf("Copying resources from %s", sd.ResDir)
		if err := copyTree(sd.ResDir, filepath.Join(dir, "OtherResources")); err != nil {
			return fmt.Errorf("copying res/: %w", err)
		}
	}
	return nil
}

func dedupe(v []string) []string {
	seen := map[string]bool{}
	out := v[:0]
	for _, s := range v {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (c *Converter) frontMatter(sd *StarDict) string {
	info := sd.Info
	var b strings.Builder
	b.WriteString(`<d:entry id="` + frontMatterID + `" d:title="` + escAttr(keyText(c.opts.Name)) + `">`)
	b.WriteString(`<h1 class="hw">`)
	escText(&b, c.opts.Name)
	b.WriteString(`</h1><div class="fm">`)
	if info.Description != "" {
		b.WriteString(`<div class="fm-desc">`)
		c.markupToXHTML(&b, strings.ReplaceAll(info.Description, `\n`, "<br>"), modeHTML)
		b.WriteString(`</div>`)
	}
	b.WriteString(`<table class="fm-meta">`)
	row := func(k, v string) {
		if strings.TrimSpace(v) == "" {
			return
		}
		b.WriteString(`<tr><th>`)
		escText(&b, k)
		b.WriteString(`</th><td>`)
		escText(&b, v)
		b.WriteString(`</td></tr>`)
	}
	row("Original title", info.BookName)
	row("Author", info.Author)
	row("Email", info.Email)
	row("Website", info.Website)
	row("Date", info.Date)
	row("Entries", fmt.Sprintf("%d", len(sd.Entries)))
	if len(sd.Syns) > 0 {
		row("Synonyms", fmt.Sprintf("%d", len(sd.Syns)))
	}
	row("StarDict version", info.Version)
	row("Source", filepath.Base(info.Path))
	row("Converted", time.Now().Format("2006-01-02")+" with StarDict2Mac")
	b.WriteString(`</table></div></d:entry>` + "\n")
	return b.String()
}

// Apple's own dictionaries set body { font-family: ui-serif; font-size: 12pt }.
// For CJK text we add the matching system serif so Han characters get the
// right regional glyph forms.
func defaultFontStack(lang string) string {
	switch lang {
	case "zh-Hant":
		return `ui-serif, "Songti TC", "Songti SC", serif`
	case "zh-Hans":
		return `ui-serif, "Songti SC", "Songti TC", serif`
	case "ja":
		return `ui-serif, "Hiragino Mincho ProN", "Hiragino Mincho Pro", serif`
	case "ko":
		return `ui-serif, "AppleMyungjo", "Apple SD Gothic Neo", serif`
	}
	return `ui-serif, serif`
}

var fontStacks = map[string]string{
	"system":    `-apple-system, "Helvetica Neue", "PingFang TC", "PingFang SC", "Hiragino Sans", "Apple SD Gothic Neo", sans-serif`,
	"serif":     `"Iowan Old Style", "Palatino", "Songti TC", "Songti SC", "Hiragino Mincho ProN", "AppleMyungjo", serif`,
	"songti-tc": `"Songti TC", "Songti SC", "STSong", "Hiragino Mincho ProN", serif`,
	"songti-sc": `"Songti SC", "Songti TC", "STSong", "Hiragino Mincho ProN", serif`,
	"kaiti":     `"Kaiti TC", "Kaiti SC", "STKaiti", "BiauKai", serif`,
	"pingfang":  `"PingFang TC", "PingFang SC", -apple-system, sans-serif`,
	"mincho":    `"Hiragino Mincho ProN", "Songti TC", serif`,
}

// fontStack returns the CSS font-family value for a font choice.
func fontStack(key, lang string) string {
	if s, ok := fontStacks[key]; ok {
		return s
	}
	key = strings.TrimSpace(key)
	if key == "" || key == "default" {
		return defaultFontStack(lang)
	}
	// custom font name typed by the user
	clean := strings.NewReplacer(`"`, "", ";", "", "{", "", "}", "", "<", "", ">", "").Replace(key)
	return `"` + clean + `", ` + defaultFontStack(lang)
}

func BuildCSS(opts Options) string {
	size := opts.FontSize
	if size < 50 || size > 300 {
		size = 100
	}
	return fmt.Sprintf(`@charset "UTF-8";
@namespace d url(http://www.apple.com/DTDs/DictionaryService-1.0.rng);

/* Same base as Apple's built-in dictionaries */
body {
	font-size: 12pt;
	font-family: %s;
}
d|entry {
	font-size: %d%%;
	line-height: 1.65;
}
h1.hw {
	font-size: 158%%;
	font-weight: 500;
	margin: 0 0 0.35em 0;
	line-height: 1.25;
}
.sd { word-wrap: break-word; }
.sd-phon, .sd-yomi { color: #666; margin-bottom: .3em; }
.sd u { text-decoration-thickness: 1px; text-underline-offset: 0.18em; text-decoration-color: rgba(128,128,128,.7); }
.sd img { max-width: 100%%; }
.sd table { border-collapse: collapse; }
.sd td, .sd th { padding: 2px 6px; vertical-align: top; }
.sd blockquote, .xdxf-bq { margin: .2em 0 .2em 1.2em; }
a.xref { text-decoration: none; }
a.xref:hover { text-decoration: underline; }
.xdxf-tr { color: #666; }
.xdxf-ex { color: #555; font-style: italic; }
.xdxf-abr { color: #2a7a2a; font-style: italic; }
.xdxf-co { color: #666; }
.fm-meta th { text-align: left; color: #666; font-weight: normal; padding-right: 1em; }

html.apple_client-panel d|entry { font-size: %d%%; line-height: 1.5; }
html.apple_client-panel h1.hw { font-size: 140%%; }

@media (prefers-color-scheme: dark) {
	.sd-phon, .sd-yomi, .xdxf-tr, .xdxf-co, .fm-meta th { color: #aaa; }
	.xdxf-ex { color: #bbb; }
	.sd [style*="color:blue"], .sd [style*="color: blue"] { color: #6cb4ff !important; }
	.sd [style*="color:red"] { color: #ff7b72 !important; }
	.sd [style*="color:green"] { color: #7ee787 !important; }
	.sd [style*="color:navy"], .sd [style*="color:darkblue"] { color: #79c0ff !important; }
	.sd [style*="color:black"], .sd [style*="color:#000"] { color: inherit !important; }
}
`, fontStack(opts.Font, opts.Lang), size, size*9/10)
}

func plistEsc(s string) string {
	var b strings.Builder
	escText(&b, s)
	return b.String()
}

func BuildPlist(info IfoInfo, opts Options) string {
	copyright := info.Author
	if copyright == "" {
		copyright = info.BookName
	}
	manu := info.Author
	if manu == "" {
		manu = "StarDict"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>English</string>
	<key>CFBundleIdentifier</key>
	<string>` + plistEsc(opts.BundleID) + `</string>
	<key>CFBundleName</key>
	<string>` + plistEsc(opts.Name) + `</string>
	<key>CFBundleDisplayName</key>
	<string>` + plistEsc(opts.Name) + `</string>
	<key>CFBundleShortVersionString</key>
	<string>1.0</string>
	<key>DCSDictionaryCopyright</key>
	<string>` + plistEsc(copyright) + `</string>
	<key>DCSDictionaryManufacturerName</key>
	<string>` + plistEsc(manu) + `</string>
	<key>DCSDictionaryFrontMatterReferenceID</key>
	<string>` + frontMatterID + `</string>
	<key>DCSDictionaryUseSystemAppearance</key>
	<true/>
</dict>
</plist>
`
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(p, target, 0644)
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
