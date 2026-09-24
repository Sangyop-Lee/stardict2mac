#!/usr/bin/env python3
import io, struct, sys
from PIL import Image
src, out = sys.argv[1], sys.argv[2]
img = Image.open(src).convert('RGBA')
chunks = b''
for typ, size in [(b'ic10', 1024), (b'ic09', 512), (b'ic08', 256), (b'ic07', 128), (b'ic14', 512), (b'ic13', 256), (b'ic12', 64), (b'ic11', 32)]:
    b = io.BytesIO(); img.resize((size, size), Image.LANCZOS).save(b, 'PNG'); d = b.getvalue()
    chunks += typ + struct.pack('>I', len(d) + 8) + d
open(out, 'wb').write(b'icns' + struct.pack('>I', len(chunks) + 8) + chunks)
