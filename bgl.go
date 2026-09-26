package main

// Reader for Babylon glossaries (.bgl).
//
// Layout: a 6-byte header (signature 0x12340001/2 and the offset of a gzip
// stream), then a gzip stream made of blocks. Each block starts with one byte:
// low nibble = block type, high nibble = size of the length field.
// Block types: 0 = settings, 1/7/10 = entry, 11 = entry with long fields,
// 2 = resource file, 3 = property, 4 = end.
// The glossary is exposed as an in-memory StarDict ("h" = HTML) so the rest of
// the converter works unchanged.

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"stardict2mac/internal/xtext/encoding"
	"stardict2mac/internal/xtext/encoding/charmap"
	"stardict2mac/internal/xtext/encoding/japanese"
	"stardict2mac/internal/xtext/encoding/korean"
	"stardict2mac/internal/xtext/encoding/simplifiedchinese"
	"stardict2mac/internal/xtext/encoding/traditionalchinese"
)

func isBGL(p string) bool { return strings.EqualFold(filepath.Ext(p), ".bgl") }

// Babylon charset codes → legacy Windows code pages.
func bglCharset(code byte) encoding.Encoding {
	switch code {
	case 0x41, 0x42:
		return charmap.Windows1252
	case 0x43:
		return charmap.Windows1250
	case 0x44:
		return charmap.Windows1251
	case 0x45:
		return japanese.ShiftJIS
	case 0x46:
		return traditionalchinese.Big5
	case 0x47:
		return simplifiedchinese.GBK
	case 0x48:
		return charmap.Windows1257
	case 0x49:
		return charmap.Windows1253
	case 0x4A:
		return korean.EUCKR
	case 0x4B:
		return charmap.Windows1254
	case 0x4C:
		return charmap.Windows1255
	case 0x4D:
		return charmap.Windows1256
	case 0x4E:
		return charmap.Windows874
	}
	return charmap.Windows1252
}

type bglReader struct {
	utf8Flag      bool
	defaultCS     byte
	sourceCS      byte
	targetCS      byte
	title         []byte
	author        []byte
	email         []byte
	copyright     []byte
	description   []byte
	entries       []bglEntry
	resources     map[string][]byte
	sawEntryBlock bool
}

type bglEntry struct {
	key  []byte
	defi []byte
	alts [][]byte
}

func (r *bglReader) decode(b []byte, cs byte) string {
	if r.utf8Flag || utf8.Valid(b) {
		return string(b)
	}
	if cs == 0 {
		cs = r.defaultCS
	}
	out, err := bglCharset(cs).NewDecoder().Bytes(b)
	if err != nil {
		return strings.ToValidUTF8(string(b), "�")
	}
	return string(out)
}

func readBGLBlocks(path string) (*bglReader, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 6 {
		return nil, errors.New("not a Babylon .bgl file (too short)")
	}
	sig := binary.BigEndian.Uint32(raw[:4])
	if sig != 0x12340001 && sig != 0x12340002 {
		return nil, errors.New("not a Babylon .bgl file (unknown signature)")
	}
	off := int(binary.BigEndian.Uint16(raw[4:6]))
	if off < 6 || off >= len(raw) {
		return nil, errors.New("corrupt .bgl header")
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw[off:]))
	if err != nil {
		return nil, fmt.Errorf("corrupt .bgl data: %w", err)
	}
	zr.Multistream(false)
	data, err := io.ReadAll(zr)
	if err != nil && len(data) == 0 {
		return nil, fmt.Errorf("corrupt .bgl data: %w", err)
	}
	// A damaged tail is tolerated: we keep whatever was decompressed.

	r := &bglReader{resources: map[string][]byte{}}
	pos := 0
	for pos < len(data) {
		b := data[pos]
		pos++
		typ := b & 0x0f
		lenSize := int(b >> 4)
		var n int
		if lenSize < 4 {
			k := lenSize + 1
			if pos+k > len(data) {
				break
			}
			for i := 0; i < k; i++ {
				n = n<<8 | int(data[pos+i])
			}
			pos += k
		} else {
			n = lenSize - 4
		}
		if n < 0 || pos+n > len(data) {
			break
		}
		blk := data[pos : pos+n]
		pos += n
		switch typ {
		case 0:
			if len(blk) >= 2 && blk[0] == 0x08 {
				r.defaultCS = blk[1]
			}
		case 1, 7, 10, 13:
			r.parseShortEntry(blk)
		case 11:
			r.parseLongEntry(blk)
		case 2:
			if len(blk) >= 1 {
				nl := int(blk[0])
				if 1+nl <= len(blk) {
					name := string(blk[1 : 1+nl])
					r.resources[name] = blk[1+nl:]
				}
			}
		case 3:
			if len(blk) < 2 {
				continue
			}
			code := binary.BigEndian.Uint16(blk[:2])
			v := blk[2:]
			switch code {
			case 0x01:
				r.title = v
			case 0x02:
				r.author = v
			case 0x03:
				r.email = v
			case 0x04:
				r.copyright = v
			case 0x09:
				r.description = v
			case 0x11:
				var flags uint32
				for _, c := range v {
					flags = flags<<8 | uint32(c)
				}
				r.utf8Flag = flags&0x8000 != 0
			case 0x1a:
				if len(v) > 0 {
					r.sourceCS = v[0]
				}
			case 0x1b:
				if len(v) > 0 {
					r.targetCS = v[0]
				}
			}
		case 4:
			pos = len(data)
		}
	}
	if len(r.entries) == 0 {
		return nil, errors.New("no entries found in this .bgl file")
	}
	return r, nil
}

func (r *bglReader) parseShortEntry(b []byte) {
	p := 0
	if p >= len(b) {
		return
	}
	kl := int(b[p])
	p++
	if p+kl > len(b) {
		return
	}
	key := b[p : p+kl]
	p += kl
	if p+2 > len(b) {
		return
	}
	dl := int(binary.BigEndian.Uint16(b[p:]))
	p += 2
	if p+dl > len(b) {
		dl = len(b) - p
	}
	defi := b[p : p+dl]
	p += dl
	var alts [][]byte
	for p < len(b) {
		n := int(b[p])
		p++
		if p+n > len(b) {
			break
		}
		alts = append(alts, b[p:p+n])
		p += n
	}
	r.entries = append(r.entries, bglEntry{key, defi, alts})
}

func (r *bglReader) parseLongEntry(b []byte) {
	be := func(x []byte) int {
		n := 0
		for _, c := range x {
			n = n<<8 | int(c)
		}
		return n
	}
	p := 0
	if p+5 > len(b) {
		return
	}
	kl := be(b[p : p+5])
	p += 5
	if kl < 0 || p+kl > len(b) {
		return
	}
	key := b[p : p+kl]
	p += kl
	if p+4 > len(b) {
		return
	}
	nAlts := be(b[p : p+4])
	p += 4
	var alts [][]byte
	for i := 0; i < nAlts && p+4 <= len(b); i++ {
		n := be(b[p : p+4])
		p += 4
		if n < 0 || p+n > len(b) {
			return
		}
		alts = append(alts, b[p:p+n])
		p += n
	}
	if p+4 > len(b) {
		return
	}
	dl := be(b[p : p+4])
	p += 4
	if dl < 0 || p+dl > len(b) {
		dl = len(b) - p
	}
	r.entries = append(r.entries, bglEntry{key, b[p : p+dl], alts})
}

var (
	bglKeyDollar  = regexp.MustCompile(`\$\d+\$?$`)
	bglCharsetTag = regexp.MustCompile(`(?is)<charset\s+c\s*=\s*["']?(\w)["']?\s*>(.*?)</charset>`)
	bglHexCodes   = regexp.MustCompile(`([0-9A-Fa-f]{1,6});`)
)

// cleanDefi turns a raw Babylon definition into HTML.
func (r *bglReader) cleanDefi(b []byte) string {
	// Everything after 0x14 is a list of binary fields (part of speech, …).
	if i := bytes.IndexByte(b, 0x14); i >= 0 {
		b = b[:i]
	}
	s := r.decode(b, r.targetCS)
	s = bglCharsetTag.ReplaceAllStringFunc(s, func(m string) string {
		sm := bglCharsetTag.FindStringSubmatch(m)
		if strings.EqualFold(sm[1], "T") {
			return bglHexCodes.ReplaceAllStringFunc(sm[2], func(h string) string {
				v, err := strconv.ParseUint(strings.TrimSuffix(h, ";"), 16, 32)
				if err != nil || !utf8.ValidRune(rune(v)) {
					return ""
				}
				return string(rune(v))
			})
		}
		return sm[2]
	})
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n\x00 ")
	s = strings.ReplaceAll(s, "\n", "<br>")
	return s
}

func (r *bglReader) cleanKey(b []byte) string {
	s := r.decode(b, r.sourceCS)
	s = bglCharsetTag.ReplaceAllString(s, "$2")
	s = bglKeyDollar.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// ReadBGLInfo returns the glossary's metadata as IfoInfo.
func ReadBGLInfo(path string) (IfoInfo, error) {
	r, err := readBGLBlocks(path)
	if err != nil {
		return IfoInfo{}, err
	}
	return r.info(path), nil
}

func (r *bglReader) info(path string) IfoInfo {
	txt := func(b []byte) string {
		s := strings.TrimRight(r.decode(b, r.targetCS), "\x00 \r\n")
		return strings.TrimSpace(s)
	}
	info := IfoInfo{
		Path:             path,
		Base:             strings.TrimSuffix(path, filepath.Ext(path)),
		Version:          "Babylon",
		WordCount:        len(r.entries),
		BookName:         txt(r.title),
		Author:           txt(r.author),
		Email:            txt(r.email),
		Copyright:        txt(r.copyright),
		SameTypeSequence: "h",
		Format:           "bgl",
		Raw:              map[string]string{},
	}
	desc := strings.ReplaceAll(txt(r.description), "\r\n", "\n")
	info.Description = strings.ReplaceAll(desc, "\n", "<br>")
	if info.BookName == "" {
		info.BookName = filepath.Base(info.Base)
	}
	return info
}

// OpenBGL reads a .bgl file into an in-memory StarDict.
func OpenBGL(path, tmpDir string, progress func(string)) (*StarDict, error) {
	if progress != nil {
		progress("Reading Babylon glossary " + filepath.Base(path))
	}
	r, err := readBGLBlocks(path)
	if err != nil {
		return nil, err
	}
	sd := &StarDict{Info: r.info(path)}
	var buf bytes.Buffer
	for _, e := range r.entries {
		key := r.cleanKey(e.key)
		if key == "" {
			continue
		}
		defi := r.cleanDefi(e.defi)
		idx := uint32(len(sd.Entries))
		sd.Entries = append(sd.Entries, IdxEntry{Word: key, Offset: uint64(buf.Len()), Size: uint32(len(defi))})
		buf.WriteString(defi)
		seen := map[string]bool{key: true}
		addSyn := func(w string) {
			w = strings.TrimSpace(w)
			if w != "" && !seen[w] {
				seen[w] = true
				sd.Syns = append(sd.Syns, SynEntry{Word: w, Index: idx})
			}
		}
		for _, a := range e.alts {
			addSyn(r.cleanKey(a))
		}
		// "akaṭa, akata" → also searchable as each form.
		if strings.Contains(key, ", ") {
			parts := strings.Split(key, ", ")
			if len(parts) <= 6 {
				for _, p := range parts {
					addSyn(p)
				}
			}
		}
	}
	if len(sd.Entries) == 0 {
		return nil, errors.New("no usable entries in this .bgl file")
	}
	sd.Info.WordCount = len(sd.Entries)
	sd.Info.SynWordCount = len(sd.Syns)
	sd.dict = bytes.NewReader(buf.Bytes())

	if len(r.resources) > 0 && tmpDir != "" {
		dir, err := os.MkdirTemp(tmpDir, "bgl-res-")
		if err == nil {
			for name, data := range r.resources {
				clean := filepath.Base(filepath.Clean("/" + name))
				if clean == "." || clean == "/" || strings.HasPrefix(clean, ".") {
					continue
				}
				os.WriteFile(filepath.Join(dir, clean), data, 0644)
			}
			sd.ResDir = dir
			sd.closers = append(sd.closers, dirRemover(dir))
		}
	}
	return sd, nil
}

type dirRemover string

func (d dirRemover) Close() error { return os.RemoveAll(string(d)) }
