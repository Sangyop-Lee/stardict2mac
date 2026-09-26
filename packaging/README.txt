StarDict2Mac
============

Converts StarDict dictionaries (.ifo + .idx + .dict/.dict.dz, optionally .syn)
and Babylon glossaries (.bgl) into dictionaries for macOS Dictionary.app, Look Up (Force Click / three-finger
tap) and ⌃⌘D. Apple Silicon and Intel Macs, macOS 11 or later.
No Xcode, Python or Homebrew needed.

Install
-------
1. Unzip, and move StarDict2Mac.app to your Applications folder.
2. Double-click it. StarDict2Mac is free software and is not notarised by Apple,
   so the first time macOS will refuse to open it. Then:
     - macOS 15 or later: open System Settings → Privacy & Security, scroll
       down, and click "Open Anyway" next to StarDict2Mac. Confirm.
     - macOS 14 or earlier: Control-click (right-click) the app → Open → Open.
   You only need to do this once.
3. Its window opens in your web browser. (It has no Dock icon; it runs only on
   your Mac at 127.0.0.1 and quits by itself about 15 minutes after you close
   the page, or when you press Quit.)

One-time setup
--------------
StarDict2Mac uses Apple's free Dictionary Development Kit for the final build
step. Apple's terms don't allow including it in other apps, so it's set up
once, separately. The first time you convert a dictionary, choose one of:
  - click "Download it now" (about 2 MB, fetched from a public copy of the kit
    in Additional Tools for Xcode 14.1; every file's fingerprint is checked), or
  - download "Additional Tools for Xcode" from Apple
    (https://developer.apple.com/download/all/, free Apple ID), open it, and
    choose its Utilities › Dictionary Development Kit folder.

Use
---
1. Choose a StarDict .ifo file, a Babylon .bgl file, the folder containing
   one, or a .tar.gz / .tar.bz2 / .zip archive of a dictionary.
2. Check the name, font and options; look at the preview.
3. Click Convert. When done, click "Open Dictionary", then in
   Dictionary → Settings… tick the new dictionary.

Big dictionaries (hundreds of thousands of entries) take a while; you get a
notification when it's finished. To remove a dictionary, delete it from
~/Library/Dictionaries (Finder → Go → Go to Folder…).

Supported content
-----------------
StarDict: plain text, Pango markup, HTML, XDXF, phonetic fields and images in a
res/ folder. Babylon: definitions, alternate spellings, embedded images, and
legacy encodings (Big5, GBK, Shift-JIS, EUC-KR, Windows code pages). Sound files
are skipped. Cross-references become clickable links, and
synonyms (.syn) become searchable. Chinese, Japanese and Korean dictionaries
get the matching system serif font automatically.

Command line (optional)
-----------------------
  A=/Applications/StarDict2Mac.app/Contents/MacOS/StarDict2Mac
  $A setup-kit                 # one-time download of Apple's kit
  $A convert path/to/dict.ifo  # convert and install
  $A convert --help            # options: --name, --font, --no-install, ...

License
-------
MIT (see LICENSE.txt). Third-party notices: THIRD_PARTY_NOTICES.txt.
