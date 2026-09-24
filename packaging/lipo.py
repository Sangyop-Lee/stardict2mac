#!/usr/bin/env python3
"""Minimal `lipo -create`: merge thin Mach-O binaries into a universal one."""
import struct, sys
out, ins = sys.argv[1], sys.argv[2:]
datas = [open(p, 'rb').read() for p in ins]
ALIGN = 14  # 16 KiB
hdr = struct.pack('>II', 0xCAFEBABE, len(datas))
off = 4096
entries, body = b'', b''
pos = off
blobs = []
for d in datas:
    magic, cputype, cpusub = struct.unpack('<Iii', d[:12])
    assert magic == 0xFEEDFACF, 'not a 64-bit Mach-O'
    pos = (pos + (1 << ALIGN) - 1) & ~((1 << ALIGN) - 1)
    entries += struct.pack('>iiIII', cputype, cpusub & 0x00FFFFFF, pos, len(d), ALIGN)
    blobs.append((pos, d)); pos += len(d)
buf = bytearray(pos)
buf[:8 + len(entries)] = hdr + entries
for p, d in blobs:
    buf[p:p + len(d)] = d
open(out, 'wb').write(buf)
