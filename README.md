# StarDict2Mac

A small macOS app that converts **StarDict** dictionaries into dictionaries for
**Dictionary.app** and system-wide **Look Up** (Force Click, three-finger tap, ⌃⌘D).

- Double-click app with a simple window: pick a dictionary, preview entries, convert, install.
- Handles plain text, Pango markup, HTML, XDXF, phonetics, `res/` images, `.syn` synonyms,
  64-bit indexes, and `.tar.gz` / `.tar.bz2` / `.zip` archives.
- Cross-references become links; CJK dictionaries get the right system serif.
- Universal binary (Apple Silicon + Intel), macOS 11+. No Xcode, Python or Homebrew.

User instructions are in [`packaging/README.txt`](packaging/README.txt) (shipped inside the release zip).

## How it works

1. **Read** the StarDict files (`stardict.go`).
2. **Convert** each entry to well-formed XHTML (`markup.go`, using a vendored copy of
   `golang.org/x/net/html` for tolerant parsing) and write Apple's Dictionary
   Development Kit sources — XML, CSS, Info.plist (`appledict.go`).
3. **Build** with Apple's `build_dict.sh` (`build.go`) and install into
   `~/Library/Dictionaries`.

The UI (`ui.html`, `server.go`) is a local web page served on `127.0.0.1`; native file
pickers are shown with `osascript`.

### Apple's Dictionary Development Kit

The kit isn't included, because Apple's terms don't allow redistribution; it's set up on first use. `kit.go` looks for it in:

1. `$STARDICT2MAC_DDK`
2. a folder the user chose (Additional Tools for Xcode → Utilities → Dictionary Development Kit)
3. common install locations such as `/Applications/Utilities/Dictionary Development Kit`
4. a copy the app downloaded earlier

If none is found, the user can download it with one click. Files come from
[nanoskript/dictionary-development-kit](https://github.com/nanoskript/dictionary-development-kit)
at a pinned commit, and each file's SHA-256 is verified.

## Building

Needs Go ≥ 1.22 and Python 3 with Pillow. Works on macOS or Linux.

```sh
sh packaging/build_app.sh            # → dist/StarDict2Mac.app and dist/StarDict2Mac-<version>.zip
```

On macOS the bundle is ad-hoc signed with `codesign`. Elsewhere it uses
[`rcodesign`](https://github.com/indygreg/apple-platform-rs) if you set `RCODESIGN=/path/to/rcodesign`.

To ship without Gatekeeper warnings you need an Apple Developer ID. See
`packaging/notarize.sh`.

### Command line

```sh
StarDict2Mac                          # open the window
StarDict2Mac setup-kit [folder]       # download or locate Apple's kit
StarDict2Mac convert [options] <path> # .ifo, folder or archive
StarDict2Mac sources [options] <path> <outdir>   # only write the DDK sources
```

## License

MIT, © 2026 Sangyop Lee. See `LICENSE` and `THIRD_PARTY_NOTICES.txt`.
