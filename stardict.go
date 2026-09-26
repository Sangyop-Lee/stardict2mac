package main

// StarDict reader: .ifo / .idx[.gz] / .dict[.dz] / .syn[.dz]
// Format reference: StarDict "doc/StarDictFileFormat".

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type IfoInfo struct {
	Path             string
	Base             string // path without extension
	Version          string
	WordCount        int
	SynWordCount     int
	IdxFileSize      int64
	IdxOffsetBits    int
	BookName         string
	Author           string
	Email            string
	Website          string
	Description      string
	Date             string
	SameTypeSequence string
	DictType         string
	Copyright        string
	Format           string // "" for StarDict, "bgl" for Babylon
	Raw              map[string]string
}

type IdxEntry struct {
	Word   string
	Offset uint64
	Size   uint32
}

type SynEntry struct {
	Word  string
	Index uint32
}

type Field struct {
	Type byte
	Data []byte
}

type StarDict struct {
	Info    IfoInfo
	Entries []IdxEntry
	Syns    []SynEntry
	dict    io.ReaderAt
	closers []io.Closer
	ResDir  string // "res" directory beside the .ifo, if any
}

func ParseIfo(path string) (IfoInfo, error) {
	var info IfoInfo
	b, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "StarDict's dict ifo file") {
		return info, errors.New("not a StarDict .ifo file (missing magic header)")
	}
	info.Path = path
	info.Base = strings.TrimSuffix(path, filepath.Ext(path))
	info.Raw = map[string]string{}
	info.IdxOffsetBits = 32
	for _, ln := range lines[1:] {
		i := strings.IndexByte(ln, '=')
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(ln[:i])
		v := strings.TrimSpace(ln[i+1:])
		info.Raw[k] = v
		switch k {
		case "version":
			info.Version = v
		case "wordcount":
			info.WordCount, _ = strconv.Atoi(v)
		case "synwordcount":
			info.SynWordCount, _ = strconv.Atoi(v)
		case "idxfilesize":
			info.IdxFileSize, _ = strconv.ParseInt(v, 10, 64)
		case "idxoffsetbits":
			if n, e := strconv.Atoi(v); e == nil && (n == 32 || n == 64) {
				info.IdxOffsetBits = n
			}
		case "bookname":
			info.BookName = v
		case "author":
			info.Author = v
		case "email":
			info.Email = v
		case "website":
			info.Website = v
		case "description":
			info.Description = v
		case "date":
			info.Date = v
		case "sametypesequence":
			info.SameTypeSequence = v
		case "dicttype":
			info.DictType = v
		}
	}
	if info.BookName == "" {
		info.BookName = filepath.Base(info.Base)
	}
	return info, nil
}

// firstExisting returns the first path (base+suffix) that exists.
func firstExisting(base string, suffixes ...string) string {
	for _, s := range suffixes {
		p := base + s
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func readMaybeGzip(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var magic [2]byte
	n, _ := io.ReadFull(f, magic[:])
	f.Seek(0, io.SeekStart)
	if n == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(bufio.NewReaderSize(f, 1<<20))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return io.ReadAll(zr)
	}
	return io.ReadAll(f)
}

// OpenStarDict opens the dictionary described by an .ifo path.
// tmpDir is used to decompress .dict.dz so that entries can be read randomly.
func OpenStarDict(ifoPath, tmpDir string, progress func(string)) (*StarDict, error) {
	if isBGL(ifoPath) {
		return OpenBGL(ifoPath, tmpDir, progress)
	}
	info, err := ParseIfo(ifoPath)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(info.DictType, "treedict") {
		return nil, errors.New("tree dictionaries (.tdx) are not supported")
	}
	sd := &StarDict{Info: info}

	// ---- .idx
	idxPath := firstExisting(info.Base, ".idx", ".idx.gz", ".IDX", ".idx.dz")
	if idxPath == "" {
		return nil, fmt.Errorf("cannot find %s.idx", filepath.Base(info.Base))
	}
	if progress != nil {
		progress("Reading index " + filepath.Base(idxPath))
	}
	idx, err := readMaybeGzip(idxPath)
	if err != nil {
		return nil, fmt.Errorf("reading idx: %w", err)
	}
	offBytes := 4
	if info.IdxOffsetBits == 64 {
		offBytes = 8
	}
	entries := make([]IdxEntry, 0, info.WordCount)
	pos := 0
	for pos < len(idx) {
		z := bytes.IndexByte(idx[pos:], 0)
		if z < 0 {
			return nil, errors.New("corrupt .idx (unterminated word)")
		}
		word := string(idx[pos : pos+z])
		pos += z + 1
		if pos+offBytes+4 > len(idx) {
			return nil, errors.New("corrupt .idx (truncated record)")
		}
		var off uint64
		if offBytes == 8 {
			off = binary.BigEndian.Uint64(idx[pos:])
		} else {
			off = uint64(binary.BigEndian.Uint32(idx[pos:]))
		}
		size := binary.BigEndian.Uint32(idx[pos+offBytes:])
		pos += offBytes + 4
		entries = append(entries, IdxEntry{Word: word, Offset: off, Size: size})
	}
	sd.Entries = entries

	// ---- .syn (optional)
	if synPath := firstExisting(info.Base, ".syn", ".syn.dz", ".syn.gz", ".SYN"); synPath != "" {
		if progress != nil {
			progress("Reading synonyms " + filepath.Base(synPath))
		}
		syn, err := readMaybeGzip(synPath)
		if err != nil {
			return nil, fmt.Errorf("reading syn: %w", err)
		}
		pos := 0
		for pos < len(syn) {
			z := bytes.IndexByte(syn[pos:], 0)
			if z < 0 || pos+z+5 > len(syn) {
				break
			}
			w := string(syn[pos : pos+z])
			i := binary.BigEndian.Uint32(syn[pos+z+1:])
			pos += z + 5
			if int(i) < len(entries) {
				sd.Syns = append(sd.Syns, SynEntry{Word: w, Index: i})
			}
		}
	}

	// ---- .dict / .dict.dz
	dictPath := firstExisting(info.Base, ".dict", ".dict.dz", ".DICT", ".dict.gz")
	if dictPath == "" {
		return nil, fmt.Errorf("cannot find %s.dict or .dict.dz", filepath.Base(info.Base))
	}
	f, err := os.Open(dictPath)
	if err != nil {
		return nil, err
	}
	var magic [2]byte
	io.ReadFull(f, magic[:])
	f.Seek(0, io.SeekStart)
	if magic[0] == 0x1f && magic[1] == 0x8b {
		if progress != nil {
			progress("Decompressing " + filepath.Base(dictPath))
		}
		zr, err := gzip.NewReader(bufio.NewReaderSize(f, 1<<20))
		if err != nil {
			f.Close()
			return nil, err
		}
		tmp, err := os.CreateTemp(tmpDir, "dict-*.raw")
		if err != nil {
			f.Close()
			return nil, err
		}
		os.Remove(tmp.Name()) // unlinked; lives until closed
		if _, err := io.Copy(tmp, zr); err != nil {
			f.Close()
			tmp.Close()
			return nil, fmt.Errorf("decompressing dict: %w", err)
		}
		f.Close()
		sd.dict = tmp
		sd.closers = append(sd.closers, tmp)
	} else {
		sd.dict = f
		sd.closers = append(sd.closers, f)
	}

	if st, err := os.Stat(filepath.Join(filepath.Dir(ifoPath), "res")); err == nil && st.IsDir() {
		sd.ResDir = filepath.Join(filepath.Dir(ifoPath), "res")
	}
	return sd, nil
}

func (sd *StarDict) Close() {
	for _, c := range sd.closers {
		c.Close()
	}
}

func (sd *StarDict) RawData(i int) ([]byte, error) {
	e := sd.Entries[i]
	buf := make([]byte, e.Size)
	_, err := sd.dict.ReadAt(buf, int64(e.Offset))
	if err != nil && !(errors.Is(err, io.EOF)) {
		return nil, err
	}
	return buf, nil
}

// Fields splits raw entry data into typed fields.
func (sd *StarDict) Fields(data []byte) []Field {
	var out []Field
	seq := sd.Info.SameTypeSequence
	if seq != "" {
		pos := 0
		for k := 0; k < len(seq) && pos <= len(data); k++ {
			t := seq[k]
			last := k == len(seq)-1
			if last {
				out = append(out, Field{t, data[pos:]})
				break
			}
			if isLowerType(t) {
				z := bytes.IndexByte(data[pos:], 0)
				if z < 0 {
					out = append(out, Field{t, data[pos:]})
					break
				}
				out = append(out, Field{t, data[pos : pos+z]})
				pos += z + 1
			} else {
				if pos+4 > len(data) {
					break
				}
				n := int(binary.BigEndian.Uint32(data[pos:]))
				pos += 4
				if pos+n > len(data) {
					n = len(data) - pos
				}
				out = append(out, Field{t, data[pos : pos+n]})
				pos += n
			}
		}
		return out
	}
	pos := 0
	for pos < len(data) {
		t := data[pos]
		pos++
		if isLowerType(t) {
			z := bytes.IndexByte(data[pos:], 0)
			if z < 0 {
				out = append(out, Field{t, data[pos:]})
				break
			}
			out = append(out, Field{t, data[pos : pos+z]})
			pos += z + 1
		} else {
			if pos+4 > len(data) {
				break
			}
			n := int(binary.BigEndian.Uint32(data[pos:]))
			pos += 4
			if pos+n > len(data) {
				n = len(data) - pos
			}
			out = append(out, Field{t, data[pos : pos+n]})
			pos += n
		}
	}
	return out
}

func isLowerType(t byte) bool { return t >= 'a' && t <= 'z' }

// ReadInfo returns the metadata of a StarDict .ifo or a Babylon .bgl file.
func ReadInfo(path string) (IfoInfo, error) {
	if isBGL(path) {
		return ReadBGLInfo(path)
	}
	return ParseIfo(path)
}

// FindIfo locates a .ifo file (or, failing that, a single .bgl file) inside a
// directory (recursively, shallow first).
func FindIfo(dir string) (string, error) {
	var found []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != dir {
			return filepath.SkipDir
		}
		if !d.IsDir() && (strings.EqualFold(filepath.Ext(p), ".ifo") || isBGL(p)) {
			found = append(found, p)
		}
		return nil
	})
	if len(found) == 0 {
		return "", errors.New("no StarDict (.ifo) or Babylon (.bgl) file found in " + dir)
	}
	// Use the shallowest dictionary; if several sit at that level, ask.
	depth := func(p string) int { return strings.Count(p, string(os.PathSeparator)) }
	min := depth(found[0])
	for _, p := range found[1:] {
		if d := depth(p); d < min {
			min = d
		}
	}
	var top []string
	for _, p := range found {
		if depth(p) == min {
			top = append(top, p)
		}
	}
	if len(top) == 1 {
		return top[0], nil
	}
	var names []string
	for _, p := range top {
		names = append(names, filepath.Base(p))
	}
	return "", fmt.Errorf("this folder contains several dictionaries (%s); choose one of the files instead", strings.Join(names, ", "))
}
