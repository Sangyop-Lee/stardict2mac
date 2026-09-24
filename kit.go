package main

// Locating (or downloading) Apple's Dictionary Development Kit.
//
// The kit is not bundled, because Apple's terms don't allow redistributing it.
// StarDict2Mac looks for it in this order:
//   1. $STARDICT2MAC_DDK
//   2. a folder the user pointed to (saved in Application Support)
//   3. a copy installed from Apple's "Additional Tools for Xcode"
//   4. a copy previously downloaded by StarDict2Mac (checksum-verified)
// If none exists, the user can download it (about 2 MB) with one click.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Mirror of the kit shipped with Additional Tools for Xcode 14.1, pinned to a commit.
const kitMirrorBase = "https://raw.githubusercontent.com/nanoskript/dictionary-development-kit/d4127fd9cc133b081120dec18fb7fb195c74392d/bin/"
const kitMirrorPage = "https://github.com/nanoskript/dictionary-development-kit"
const kitApplePage = "https://developer.apple.com/download/all/?q=Additional%20Tools%20for%20Xcode"

// SHA-256 of every file in the mirror's bin/ folder. Downloads that do not
// match are rejected.
var kitFiles = map[string]string{
	"add_body_record":            "851f42f3f017448820b1a7daa004428c367e08a2212396af3b1cb483d213799a",
	"add_key_index_record":       "d8d21ab75c8483feb6177946c5fb0038f92d728823f11b933f7c17465d1ef3a1",
	"add_reference_index_record": "5c2b2e11a1a1baa56f1983fdd0e1e85c8c19e161f119d162b728b85449c74a6b",
	"add_supplementary_key":      "aea17e4ea1d83a9120ba3d9db7a08baef3cafe2bce306110e2eea35566e00b52",
	"build_dict.sh":              "96c60abedd89f1932bf5a54fe65ed03f739423b0434a1afe9e0cec9aae124ffc",
	"build_key_index":            "e680f616c5106c80bf865323aa17982b899b78e3f13a57ffbf71005fe3699662",
	"build_reference_index":      "52d0abeaffacea0c3bddb4e2c787d603788d621356b98d7ca45a4075833d0e80",
	"extract_front_matter_id.pl": "6897d1ccb71fef0b526cf7bf23fc0112d0397a0c7477e5e2dfbb59cd54d8e59b",
	"extract_index.pl":           "553ffe57ce706b0420f34ed4b54f6642e793018d2405e04b13fc765305b0c721",
	"extract_property.xsl":       "f3717e856b76a2fdea5d63ca325773cb0e39de0468fd666b7b133739061989c3",
	"extract_referred_id.pl":     "a7a019681cde576d4be07f9f18c7b50d5abc8124195e62fe0bf835d859a0f044",
	"generate_dict_template.sh":  "ca29e1549542a04b6bdbb9e3af16506e29c4770846ebafc2ae4709aca57d1d3d",
	"make_body.pl":               "56610c47465fd569323cef625619d3ae64c12ecaa48f3322c2d239e4f9b80939",
	"make_dict_package":          "3782e120009ea286931eaf580372c1c9f928e8413b13a73dad3f7b946dc7c2bb",
	"make_line.pl":               "1737e1ab4d2254b4e661f025246932d54a1f538ab62a36572d5a5a3b69351fa6",
	"make_readonly.pl":           "5a33bea0cbd1ccd7fe7ade30f1d35920c05d367f075244f0bde1ed04c89bcf0b",
	"normalize_key_text":         "4f8e2e0bf2b047493b52e51c81352e90c42f658b5387f1fded102cc18fa93a92",
	"normalize_key_text.pl":      "c004cec0efbf9bfb24a505fb39fc18413e51fcc116255a6330b59a6dd190948d",
	"pick_referred_entry_id.pl":  "3ffa8473f480e678ebc0b0f318ba88c1b93c1b83731619d11f73367c06bc20bb",
	"remove_duplicate_key.pl":    "90183557e24e44ddf5696f99b11f43aece76d54858334f56d7b08dfa5d0d9f85",
	"replace_entryid_bodyid.pl":  "a60a4e3c4763866cb8a2e09e977e971ba4c4cf66c0db1f408a0ee997cf52b2b1",
}

// Files build_dict.sh actually runs; a kit folder must contain all of them.
var kitRequired = []string{
	"build_dict.sh", "extract_property.xsl", "generate_dict_template.sh", "make_line.pl",
	"make_body.pl", "extract_index.pl", "extract_referred_id.pl", "extract_front_matter_id.pl",
	"make_dict_package", "add_body_record", "replace_entryid_bodyid.pl", "normalize_key_text",
	"add_supplementary_key", "remove_duplicate_key.pl", "build_key_index",
	"pick_referred_entry_id.pl", "build_reference_index", "make_readonly.pl",
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func downloadedKitDir() string { return filepath.Join(appSupportDir(), "ddk") }
func kitPathFile() string      { return filepath.Join(appSupportDir(), "kit-path.txt") }

// normalizeKitDir accepts either the kit folder or its bin/ folder and returns
// the kit folder if it is complete.
func normalizeKitDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("empty path")
	}
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(homeDir(), p[2:])
	}
	p = filepath.Clean(p)
	if filepath.Base(p) == "bin" && fileExists(filepath.Join(p, "build_dict.sh")) {
		p = filepath.Dir(p)
	}
	var missing []string
	for _, f := range kitRequired {
		if !fileExists(filepath.Join(p, "bin", f)) {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		if len(missing) == len(kitRequired) {
			return "", fmt.Errorf("%s is not a Dictionary Development Kit folder (no bin/build_dict.sh)", p)
		}
		return "", fmt.Errorf("the kit at %s is incomplete (missing %s)", p, strings.Join(missing, ", "))
	}
	return p, nil
}

func officialKitCandidates() []string {
	h := homeDir()
	return []string{
		"/Applications/Utilities/Dictionary Development Kit",
		"/Applications/Dictionary Development Kit",
		"/Applications/Additional Tools/Utilities/Dictionary Development Kit",
		"/Applications/Additional Tools for Xcode/Utilities/Dictionary Development Kit",
		"/Developer/Extras/Dictionary Development Kit",
		"/DevTools/Utilities/Dictionary Development Kit",
		filepath.Join(h, "Applications", "Utilities", "Dictionary Development Kit"),
		filepath.Join(h, "Applications", "Dictionary Development Kit"),
		filepath.Join(h, "Downloads", "Dictionary Development Kit"),
		filepath.Join(h, "Downloads", "Additional Tools", "Utilities", "Dictionary Development Kit"),
	}
}

type KitStatus struct {
	Found  bool   `json:"found"`
	Path   string `json:"path"`
	Source string `json:"source"` // env, chosen, installed, downloaded
	Error  string `json:"error,omitempty"`
}

// FindKit looks for a usable kit without downloading anything.
func FindKit() KitStatus {
	if env := os.Getenv("STARDICT2MAC_DDK"); env != "" {
		if p, err := normalizeKitDir(env); err == nil {
			return KitStatus{true, p, "env", ""}
		} else {
			return KitStatus{false, "", "env", err.Error()}
		}
	}
	var lastErr string
	if b, err := os.ReadFile(kitPathFile()); err == nil {
		if p, err := normalizeKitDir(string(b)); err == nil {
			return KitStatus{true, p, "chosen", ""}
		} else {
			lastErr = err.Error()
		}
	}
	for _, c := range officialKitCandidates() {
		if p, err := normalizeKitDir(c); err == nil {
			return KitStatus{true, p, "installed", ""}
		}
	}
	if p, err := normalizeKitDir(downloadedKitDir()); err == nil && verifyKit(p) == nil {
		return KitStatus{true, p, "downloaded", ""}
	}
	return KitStatus{Found: false, Error: lastErr}
}

// EnsureDDK returns a usable kit directory or an explanatory error.
func EnsureDDK() (string, error) {
	st := FindKit()
	if st.Found {
		return st.Path, nil
	}
	msg := "Apple's Dictionary Development Kit is not set up yet. Download it from the app window (one click), or run: StarDict2Mac setup-kit"
	if st.Error != "" {
		msg = st.Error + ". " + msg
	}
	return "", errors.New(msg)
}

// SetKitPath remembers a user-chosen kit folder.
func SetKitPath(p string) (string, error) {
	dir, err := normalizeKitDir(p)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(appSupportDir(), 0755); err != nil {
		return "", err
	}
	// make sure scripts are executable (copies from zips sometimes lose the bit)
	for _, f := range kitRequired {
		if !strings.HasSuffix(f, ".xsl") {
			fp := filepath.Join(dir, "bin", f)
			if st, err := os.Stat(fp); err == nil && st.Mode()&0111 == 0 {
				os.Chmod(fp, 0755)
			}
		}
	}
	return dir, os.WriteFile(kitPathFile(), []byte(dir), 0644)
}

func verifyKit(dir string) error {
	for name, want := range kitFiles {
		got, err := sha256File(filepath.Join(dir, "bin", name))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s does not match its expected checksum", name)
		}
	}
	return nil
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DownloadKit fetches the kit from the pinned mirror, verifies every file's
// SHA-256, and installs it into Application Support.
func DownloadKit(progress func(done, total int, name string)) (string, error) {
	names := make([]string, 0, len(kitFiles))
	for n := range kitFiles {
		names = append(names, n)
	}
	sort.Strings(names)
	tmp := downloadedKitDir() + ".partial"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "bin"), 0755); err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	for i, name := range names {
		if progress != nil {
			progress(i, len(names), name)
		}
		data, err := fetch(client, kitMirrorBase+name)
		if err != nil {
			os.RemoveAll(tmp)
			return "", fmt.Errorf("downloading %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != kitFiles[name] {
			os.RemoveAll(tmp)
			return "", fmt.Errorf("%s failed its checksum check; the download was discarded", name)
		}
		mode := os.FileMode(0755)
		if strings.HasSuffix(name, ".xsl") {
			mode = 0644
		}
		if err := os.WriteFile(filepath.Join(tmp, "bin", name), data, mode); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
	}
	os.WriteFile(filepath.Join(tmp, "SOURCE.txt"), []byte(
		"Apple's Dictionary Development Kit (from Additional Tools for Xcode 14.1), © Apple Inc.\n"+
			"Downloaded by StarDict2Mac from "+kitMirrorPage+" on "+time.Now().Format(time.RFC3339)+"\n"), 0644)
	dst := downloadedKitDir()
	os.RemoveAll(dst)
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	// A previously chosen folder would take precedence; forget it.
	os.Remove(kitPathFile())
	if progress != nil {
		progress(len(names), len(names), "")
	}
	return dst, nil
}

func fetch(c *http.Client, url string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := c.Get(url)
		if err == nil {
			data, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
			resp.Body.Close()
			if resp.StatusCode == 200 && rerr == nil {
				return data, nil
			}
			if rerr != nil {
				lastErr = rerr
			} else {
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
		} else {
			lastErr = err
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return nil, lastErr
}
