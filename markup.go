package main

// Conversion of StarDict field types into well-formed XHTML fragments
// that Apple's Dictionary Development Kit accepts.

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"stardict2mac/internal/html"
	"stardict2mac/internal/html/atom"
)

type Options struct {
	Name         string // display name
	FileName     string // bundle file name (without .dictionary)
	BundleID     string
	Font         string // CSS font-family list
	FontSize     int    // percent
	ShowHeadword bool
	LinkXrefs    bool
	UseSynonyms  bool
	Install      bool
	Lang         string // detected script: zh-Hant, zh-Hans, ja, ko or ""
}

type Converter struct {
	opts       Options
	lookup     map[string]int // headword/synonym -> entry index
	lookupFold map[string]int // same, lower-cased
	hasRes     bool
}

// ---------------------------------------------------------------- escaping

func validXMLRune(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

// cleanText removes characters that are illegal in XML and fixes bad UTF-8.
func cleanText(s string) string {
	ok := true
	for i, r := range s {
		if r == utf8.RuneError {
			if _, n := utf8.DecodeRuneInString(s[i:]); n == 1 {
				ok = false
				break
			}
		}
		if !validXMLRune(r) {
			ok = false
			break
		}
	}
	if ok {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		if r == utf8.RuneError && n == 1 {
			b.WriteRune('�')
			continue
		}
		if validXMLRune(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func escText(b *strings.Builder, s string) {
	for _, r := range cleanText(s) {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '\r':
		default:
			b.WriteRune(r)
		}
	}
}

func escAttr(s string) string {
	var b strings.Builder
	for _, r := range cleanText(s) {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		case '\t', '\n', '\r':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// keyText makes a string safe for d:title / d:value (single line, no tabs).
func keyText(s string) string {
	s = cleanText(s)
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// ---------------------------------------------------------------- entry body

func (c *Converter) FieldsToXHTML(fields []Field) string {
	var b strings.Builder
	for _, f := range fields {
		switch f.Type {
		case 'm', 'l', 'k', 'w', 'n':
			b.WriteString(`<div class="sd-m">`)
			plainToXHTML(&b, string(f.Data))
			b.WriteString(`</div>`)
		case 't':
			b.WriteString(`<div class="sd-phon">[`)
			escText(&b, string(f.Data))
			b.WriteString(`]</div>`)
		case 'y':
			b.WriteString(`<div class="sd-yomi">`)
			escText(&b, string(f.Data))
			b.WriteString(`</div>`)
		case 'g':
			b.WriteString(`<div class="sd-g">`)
			c.markupToXHTML(&b, string(f.Data), modePango)
			b.WriteString(`</div>`)
		case 'h':
			b.WriteString(`<div class="sd-h">`)
			c.markupToXHTML(&b, string(f.Data), modeHTML)
			b.WriteString(`</div>`)
		case 'x':
			b.WriteString(`<div class="sd-x">`)
			c.markupToXHTML(&b, xdxfPrep(string(f.Data)), modeXDXF)
			b.WriteString(`</div>`)
		case 'r':
			c.resourcesToXHTML(&b, string(f.Data))
		default:
			// binary types (W sound, P picture, X reserved) are skipped
		}
	}
	return b.String()
}

func plainToXHTML(b *strings.Builder, s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Trim(s, "\n")
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if i > 0 {
			b.WriteString("<br/>")
		}
		escText(b, ln)
	}
}

func (c *Converter) resourcesToXHTML(b *strings.Builder, s string) {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if len(ln) < 3 || ln[1] != ':' {
			continue
		}
		name := ln[2:]
		if ln[0] == 'i' {
			b.WriteString(`<img src="` + escAttr(name) + `"/>`)
		}
	}
}

// ---------------------------------------------------------------- markup via HTML parser

const (
	modePango = iota
	modeHTML
	modeXDXF
)

var bodyContext = &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}

var voidElems = map[string]bool{
	"br": true, "hr": true, "img": true, "wbr": true, "area": true, "col": true,
	"embed": true, "input": true, "source": true, "track": true, "param": true,
}

var dropElems = map[string]bool{
	"script": true, "style": true, "iframe": true, "object": true, "embed": true,
	"link": true, "meta": true, "head": true, "title": true, "base": true,
	"form": true, "input": true, "button": true, "select": true, "textarea": true,
	"audio": true, "video": true, "source": true, "track": true, "param": true,
	"noscript": true, "template": true, "frame": true, "frameset": true,
}

var passElems = map[string]bool{
	"a": true, "abbr": true, "b": true, "big": true, "blockquote": true, "br": true,
	"cite": true, "code": true, "dd": true, "del": true, "dfn": true, "div": true,
	"dl": true, "dt": true, "em": true, "font": false, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "hr": true, "i": true, "img": true, "ins": true,
	"kbd": true, "li": true, "mark": true, "ol": true, "p": true, "pre": true, "q": true,
	"rp": true, "rt": true, "ruby": true, "s": true, "samp": true, "small": true,
	"span": true, "strike": true, "strong": true, "sub": true, "sup": true, "table": true,
	"tbody": true, "td": true, "tfoot": true, "th": true, "thead": true, "tr": true,
	"tt": true, "u": true, "ul": true, "var": true, "caption": true, "colgroup": true,
	"col": true, "center": true, "figure": true, "figcaption": true, "section": true,
	"article": true, "aside": true, "header": true, "footer": true, "nav": true,
	"details": true, "summary": true, "bdi": true, "bdo": true, "wbr": true, "time": true,
	"data": true, "nobr": true, "label": true, "main": true, "hgroup": true, "address": true,
}

var attrNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\-]*$`)

type attr struct{ k, v string }

type serializer struct {
	c    *Converter
	b    *strings.Builder
	mode int
}

func (c *Converter) markupToXHTML(b *strings.Builder, s string, mode int) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if mode != modeHTML {
		s = strings.Trim(s, "\n")
	}
	nodes, err := html.ParseFragment(strings.NewReader(s), bodyContext)
	if err != nil {
		plainToXHTML(b, s)
		return
	}
	ser := &serializer{c: c, b: b, mode: mode}
	for _, n := range nodes {
		ser.node(n, false)
	}
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(n)
	return sb.String()
}

func getAttr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

var xrefPunct = strings.NewReplacer("，", "", "、", "", "；", "", ",", "", "·", "", "‧", "", " ", "")

// resolve finds the entry index for a cross-reference target.
func (c *Converter) resolve(word string) (int, bool) {
	if c.lookup == nil {
		return 0, false
	}
	w := strings.TrimSpace(tagRE.ReplaceAllString(word, ""))
	for _, cand := range []string{w, xrefPunct.Replace(w), w + ".", strings.TrimSuffix(w, ".")} {
		if i, ok := c.lookup[cand]; ok {
			return i, true
		}
	}
	if c.lookupFold != nil {
		for _, cand := range []string{w, w + ".", strings.TrimSuffix(w, ".")} {
			if i, ok := c.lookupFold[strings.ToLower(cand)]; ok {
				return i, true
			}
		}
	}
	return 0, false
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func (c *Converter) linkHref(word string) (string, bool) {
	if i, ok := c.resolve(word); ok {
		return "x-dictionary:r:" + entryID(i), true
	}
	return "", false
}

func isBlue(col string) bool {
	col = strings.ToLower(strings.TrimSpace(col))
	switch col {
	case "blue", "#00f", "#0000ff", "navy", "#000080", "darkblue", "#00008b", "mediumblue", "#0000cd":
		return true
	}
	return false
}

func (s *serializer) children(n *html.Node, inPre bool) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		s.node(ch, inPre)
	}
}

func (s *serializer) text(t string, inPre bool) {
	if inPre {
		escText(s.b, t)
		return
	}
	if s.mode == modeHTML {
		escText(s.b, strings.ReplaceAll(t, "\n", " "))
		return
	}
	parts := strings.Split(t, "\n")
	for i, p := range parts {
		if i > 0 {
			s.b.WriteString("<br/>")
		}
		escText(s.b, p)
	}
}

func (s *serializer) open(name string, attrs []attr) {
	s.b.WriteByte('<')
	s.b.WriteString(name)
	for _, a := range attrs {
		s.b.WriteByte(' ')
		s.b.WriteString(a.k)
		s.b.WriteString(`="`)
		s.b.WriteString(escAttr(a.v))
		s.b.WriteByte('"')
	}
	if voidElems[name] {
		s.b.WriteString("/>")
	} else {
		s.b.WriteByte('>')
	}
}

func (s *serializer) close(name string) {
	if !voidElems[name] {
		s.b.WriteString("</" + name + ">")
	}
}

func (s *serializer) node(n *html.Node, inPre bool) {
	switch n.Type {
	case html.TextNode:
		s.text(n.Data, inPre)
		return
	case html.CommentNode, html.DoctypeNode:
		return
	case html.DocumentNode:
		s.children(n, inPre)
		return
	case html.ElementNode:
	default:
		return
	}
	name := strings.ToLower(n.Data)
	if n.Namespace != "" { // svg / math: keep only text
		s.children(n, inPre)
		return
	}
	switch s.mode {
	case modePango:
		s.pangoElem(n, name, inPre)
	case modeXDXF:
		s.xdxfElem(n, name, inPre)
	default:
		s.htmlElem(n, name, inPre)
	}
}

// generic element output with cleaned attributes
func (s *serializer) generic(n *html.Node, name string, extra []attr, inPre bool) {
	if name == "html" || name == "body" {
		s.children(n, inPre)
		return
	}
	if dropElems[name] {
		return
	}
	if !passElems[name] {
		// unknown element: keep content in a span tagged with its name
		cls := "x-" + name
		if !attrNameRE.MatchString(name) {
			cls = "x-unknown"
		}
		s.open("span", []attr{{"class", cls}})
		s.children(n, inPre)
		s.close("span")
		return
	}
	var attrs []attr
	for _, a := range n.Attr {
		k := strings.ToLower(a.Key)
		if a.Namespace != "" || !attrNameRE.MatchString(k) || strings.HasPrefix(k, "on") || k == "id" || k == "xmlns" {
			continue
		}
		v := a.Val
		if name == "a" && k == "href" {
			v = s.c.rewriteHref(v)
		}
		attrs = append(attrs, attr{k, v})
	}
	attrs = append(attrs, extra...)
	s.open(name, attrs)
	s.children(n, inPre || name == "pre")
	s.close(name)
}

func (c *Converter) rewriteHref(v string) string {
	lv := strings.ToLower(v)
	for _, p := range []string{"bword://", "entry://", "dict://", "d:"} {
		if strings.HasPrefix(lv, p) {
			w := v[len(p):]
			if i := strings.IndexByte(w, '#'); i >= 0 {
				w = w[:i]
			}
			if h, ok := c.linkHref(w); ok {
				return h
			}
			return "x-dictionary:d:" + w
		}
	}
	return v
}

func (s *serializer) htmlElem(n *html.Node, name string, inPre bool) {
	if name == "font" {
		var css []string
		if v, ok := getAttr(n, "color"); ok {
			css = append(css, "color:"+v)
		}
		if v, ok := getAttr(n, "face"); ok {
			css = append(css, "font-family:"+v)
		}
		s.open("span", styleAttr(css))
		s.children(n, inPre)
		s.close("span")
		return
	}
	s.generic(n, name, nil, inPre)
}

func styleAttr(css []string) []attr {
	if len(css) == 0 {
		return nil
	}
	return []attr{{"style", strings.Join(css, ";")}}
}

var pangoSizes = map[string]string{
	"xx-small": "xx-small", "x-small": "x-small", "small": "small", "medium": "medium",
	"large": "large", "x-large": "x-large", "xx-large": "xx-large",
	"smaller": "smaller", "larger": "larger",
}

func pangoSpanCSS(n *html.Node) []string {
	var css []string
	for _, a := range n.Attr {
		v := strings.TrimSpace(a.Val)
		switch strings.ToLower(a.Key) {
		case "foreground", "fgcolor", "color":
			css = append(css, "color:"+v)
		case "background", "bgcolor":
			css = append(css, "background-color:"+v)
		case "face", "font_family", "font-family":
			css = append(css, "font-family:"+v)
		case "weight", "font_weight":
			switch strings.ToLower(v) {
			case "ultralight", "light", "thin":
				css = append(css, "font-weight:300")
			case "normal", "book", "medium":
				css = append(css, "font-weight:normal")
			case "semibold", "bold", "ultrabold", "heavy", "ultraheavy":
				css = append(css, "font-weight:bold")
			default:
				if _, err := strconv.Atoi(v); err == nil {
					css = append(css, "font-weight:"+v)
				}
			}
		case "style", "font_style":
			switch strings.ToLower(v) {
			case "italic", "oblique":
				css = append(css, "font-style:"+strings.ToLower(v))
			}
		case "size", "font_size":
			if p, ok := pangoSizes[strings.ToLower(v)]; ok {
				css = append(css, "font-size:"+p)
			} else if num, err := strconv.Atoi(v); err == nil && num > 0 {
				pt := float64(num) / 1024
				if pt > 4 && pt < 72 {
					css = append(css, "font-size:"+strconv.FormatFloat(pt, 'f', 1, 64)+"pt")
				}
			}
		case "underline":
			if strings.ToLower(v) != "none" {
				css = append(css, "text-decoration:underline")
			}
		case "strikethrough":
			if strings.ToLower(v) == "true" {
				css = append(css, "text-decoration:line-through")
			}
		case "font_desc", "font":
			// e.g. "Sans Italic 12" – keep only style words
			lv := strings.ToLower(v)
			if strings.Contains(lv, "bold") {
				css = append(css, "font-weight:bold")
			}
			if strings.Contains(lv, "italic") {
				css = append(css, "font-style:italic")
			}
		}
	}
	// sanitize: no quotes/semicolon injection issues beyond escaping
	return css
}

func (s *serializer) pangoElem(n *html.Node, name string, inPre bool) {
	switch name {
	case "span", "font":
		css := pangoSpanCSS(n)
		if s.c.opts.LinkXrefs {
			col, _ := getAttr(n, "foreground")
			if col == "" {
				col, _ = getAttr(n, "color")
			}
			if isBlue(col) {
				if href, ok := s.c.linkHref(textContent(n)); ok {
					s.open("a", []attr{{"href", href}, {"class", "xref"}})
					s.children(n, inPre)
					s.close("a")
					return
				}
			}
		}
		s.open("span", styleAttr(css))
		s.children(n, inPre)
		s.close("span")
	case "tt":
		s.open("code", nil)
		s.children(n, inPre)
		s.close("code")
	default:
		s.generic(n, name, nil, inPre)
	}
}

// ---------------------------------------------------------------- XDXF

var xdxfTrRE = regexp.MustCompile(`(?i)<(/?)tr(\s[^>]*)?>`)

// The HTML parser would drop <tr> outside tables, so rename XDXF tags first.
func xdxfPrep(s string) string {
	return xdxfTrRE.ReplaceAllString(s, "<${1}xdxf-tr$2>")
}

func (s *serializer) xdxfElem(n *html.Node, name string, inPre bool) {
	wrap := func(tag, class string) {
		s.open(tag, []attr{{"class", class}})
		s.children(n, inPre)
		s.close(tag)
	}
	switch name {
	case "k":
		if s.c.opts.ShowHeadword {
			return // the headword is already shown as the entry title
		}
		wrap("b", "xdxf-k")
	case "opt":
		wrap("span", "xdxf-opt")
	case "xdxf-tr":
		s.b.WriteString(`<span class="xdxf-tr">[`)
		s.children(n, inPre)
		s.b.WriteString(`]</span>`)
	case "kref":
		t := textContent(n)
		if h, ok := s.c.linkHref(t); ok && s.c.opts.LinkXrefs {
			s.open("a", []attr{{"href", h}, {"class", "xref"}})
		} else {
			s.open("a", []attr{{"href", "x-dictionary:d:" + t}, {"class", "xref"}})
		}
		s.children(n, inPre)
		s.close("a")
	case "iref":
		href, _ := getAttr(n, "href")
		s.open("a", []attr{{"href", href}})
		s.children(n, inPre)
		s.close("a")
	case "abr", "abbr":
		wrap("abbr", "xdxf-abr")
	case "c":
		col, ok := getAttr(n, "c")
		if !ok {
			col = "green"
		}
		s.open("span", []attr{{"style", "color:" + col}})
		s.children(n, inPre)
		s.close("span")
	case "ex":
		wrap("span", "xdxf-ex")
	case "co":
		wrap("span", "xdxf-co")
	case "dtrn":
		wrap("span", "xdxf-dtrn")
	case "pos", "gr":
		wrap("span", "xdxf-pos")
	case "def":
		wrap("div", "xdxf-def")
	case "blockquote":
		wrap("div", "xdxf-bq")
	case "rref":
		t := textContent(n)
		s.b.WriteString(`<img src="` + escAttr(t) + `"/>`)
	case "nu", "sr":
		wrap("span", "xdxf-"+name)
	default:
		s.generic(n, name, nil, inPre)
	}
}
