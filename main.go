package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func usage() {
	fmt.Fprintf(os.Stderr, `StarDict2Mac %s — convert StarDict dictionaries for macOS Dictionary.app

Usage:
  StarDict2Mac                      open the app window (in your browser)
  StarDict2Mac convert [options] <path>
        path: an .ifo file, a folder containing one, or a .tar.gz/.tar.bz2/.zip archive
  StarDict2Mac setup-kit [folder]
        download Apple's Dictionary Development Kit (about 2 MB, checksum-verified),
        or use a kit you installed from Apple's "Additional Tools for Xcode"
  StarDict2Mac sources [options] <path> <outdir>
        only write the Dictionary Development Kit sources (XML, CSS, plist)

Options:
`, AppVersion)
	flag.PrintDefaults()
}

func main() {
	// Finder may pass -psn_… on old systems.
	var args []string
	for _, a := range os.Args[1:] {
		if !strings.HasPrefix(a, "-psn_") {
			args = append(args, a)
		}
	}
	if len(args) == 0 {
		runServer()
		return
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	name := fs.String("name", "", "dictionary display name (default: bookname from .ifo)")
	file := fs.String("file", "", "bundle file name without .dictionary (default: from .ifo file name)")
	font := fs.String("font", "default", "font: default (same as Dictionary.app), system, serif, songti-tc, songti-sc, kaiti, pingfang, mincho, or a font name")
	size := fs.Int("size", 100, "font size in percent")
	noHead := fs.Bool("no-headword", false, "do not add the headword as a title above each entry")
	noLinks := fs.Bool("no-links", false, "do not turn cross-references into links")
	noSyn := fs.Bool("no-synonyms", false, "ignore the .syn file")
	noInstall := fs.Bool("no-install", false, "save next to the source instead of installing into ~/Library/Dictionaries")
	getKit := fs.Bool("download-kit", false, "download Apple's Dictionary Development Kit first if it is not set up")
	flag.Usage = usage
	fs.Usage = usage
	switch cmd {
	case "setup-kit":
		if len(args) > 1 {
			dir, err := SetKitPath(args[1])
			if err != nil {
				fatal(err)
			}
			fmt.Println("Using Dictionary Development Kit at", dir)
			return
		}
		if st := FindKit(); st.Found {
			fmt.Println("Already set up:", st.Path)
			return
		}
		fmt.Fprintln(os.Stderr, "Downloading Apple's Dictionary Development Kit from", kitMirrorPage)
		dir, err := DownloadKit(func(d, t int, n string) {
			if n != "" {
				fmt.Fprintf(os.Stderr, "  [%d/%d] %s\n", d+1, t, n)
			}
		})
		if err != nil {
			fatal(err)
		}
		fmt.Println("Installed (all checksums verified):", dir)
		return
	case "convert", "sources":
	case "-h", "--help", "help":
		usage()
		return
	case "--version", "version":
		fmt.Println(AppVersion)
		return
	default:
		// Allow `StarDict2Mac <path>` as a shortcut for convert.
		if _, err := os.Stat(cmd); err == nil {
			args = append([]string{"convert"}, args...)
			cmd = "convert"
		} else {
			usage()
			os.Exit(2)
		}
	}
	fs.Parse(args[1:])
	rest := fs.Args()
	if (cmd == "convert" && len(rest) != 1) || (cmd == "sources" && len(rest) != 2) {
		usage()
		os.Exit(2)
	}

	ifo, err := ResolveSource(rest[0], filepath.Join(cacheDir(), "extract"))
	if err != nil {
		fatal(err)
	}
	info, err := ParseIfo(ifo)
	if err != nil {
		fatal(err)
	}
	opts := DefaultOptions(info)
	if *name != "" {
		opts.Name = *name
	}
	if *file != "" {
		opts.FileName = SafeFileName(*file, opts.Name)
	}
	opts.Font = *font
	opts.FontSize = *size
	opts.ShowHeadword = !*noHead
	opts.LinkXrefs = !*noLinks
	opts.UseSynonyms = !*noSyn
	opts.Install = !*noInstall

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if cmd == "sources" {
		out := rest[1]
		if err := os.MkdirAll(out, 0755); err != nil {
			fatal(err)
		}
		sd, err := OpenStarDict(ifo, out, func(s string) { fmt.Fprintln(os.Stderr, s) })
		if err != nil {
			fatal(err)
		}
		defer sd.Close()
		opts.Lang = DetectLang(sd)
		t0 := time.Now()
		last := -1
		err = WriteSources(ctx, sd, opts, out, func(d, t int) {
			p := d * 100 / max(t, 1)
			if p/10 != last/10 {
				fmt.Fprintf(os.Stderr, "  %d%% (%d/%d)\n", p, d, t)
				last = p
			}
		}, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
		if err != nil {
			fatal(err)
		}
		fmt.Fprintf(os.Stderr, "Wrote sources to %s in %s\n", out, time.Since(t0).Round(time.Millisecond))
		return
	}

	if !FindKit().Found {
		if !*getKit {
			fatal(fmt.Errorf("Apple's Dictionary Development Kit is not set up. Run `StarDict2Mac setup-kit` or add --download-kit"))
		}
		fmt.Fprintln(os.Stderr, "Downloading Apple's Dictionary Development Kit…")
		if _, err := DownloadKit(nil); err != nil {
			fatal(err)
		}
	}
	logToStderr = true
	j := &Job{ID: "cli", Source: ifo, Opts: opts}
	done := make(chan struct{})
	go func() {
		lastPhase := ""
		for {
			select {
			case <-done:
				return
			case <-time.After(2 * time.Second):
				s := j.Snapshot(1 << 30)
				if s.Phase != lastPhase && strings.HasPrefix(s.Phase, "Converting") {
					fmt.Fprintf(os.Stderr, "  %s\n", s.Phase)
				}
				lastPhase = s.Phase
			}
		}
	}()
	j.Run(ctx)
	close(done)
	s := j.Snapshot(1 << 30)
	if s.State != "done" {
		fatal(fmt.Errorf("%s", s.Error))
	}
	fmt.Println(s.Result)
	if opts.Install {
		fmt.Fprintln(os.Stderr, "Installed. Open Dictionary.app → Settings and tick the new dictionary if it is not enabled.")
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}
