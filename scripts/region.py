#!/usr/bin/env python3
"""Report an optical drive's DVD region state and the region of the disc in it.

Both answers matter together: an RPC-2 drive with no region set will not perform
CSS authentication, which lets a disc scan perfectly and then stall partway
through the first title. That failure looks like a bad disc and is not one.

The drive is read with a SCSI REPORT KEY rather than the kernel's DVD_AUTH
ioctl. On at least the ASUS SDRW-08D2S-U the ioctl path returns values that
decode to a plausible but wrong answer, while REPORT KEY is exact.

Nothing here writes to the drive. Setting a region is deliberately not offered:
an RPC-2 drive permits about five changes and then locks to the last one
permanently, so it should never be a side effect of asking a question.

Usage: scripts/region.py [device]    (default /dev/sr0)
"""

import ctypes
import fcntl
import os
import struct
import sys

SG_IO = 0x2285
SG_DXFER_FROM_DEV = -3

RPC_TYPE = {
    0: "no region set",
    1: "region set",
    2: "region set, last change",
    3: "region set PERMANENTLY (locked)",
}


class sg_io_hdr(ctypes.Structure):
    _fields_ = [
        ("interface_id", ctypes.c_int), ("dxfer_direction", ctypes.c_int),
        ("cmd_len", ctypes.c_ubyte), ("mx_sb_len", ctypes.c_ubyte),
        ("iovec_count", ctypes.c_ushort), ("dxfer_len", ctypes.c_uint),
        ("dxferp", ctypes.c_void_p), ("cmdp", ctypes.c_void_p),
        ("sbp", ctypes.c_void_p), ("timeout", ctypes.c_uint),
        ("flags", ctypes.c_uint), ("pack_id", ctypes.c_int),
        ("usr_ptr", ctypes.c_void_p), ("status", ctypes.c_ubyte),
        ("masked_status", ctypes.c_ubyte), ("msg_status", ctypes.c_ubyte),
        ("sb_len_wr", ctypes.c_ubyte), ("host_status", ctypes.c_ushort),
        ("driver_status", ctypes.c_ushort), ("resid", ctypes.c_int),
        ("duration", ctypes.c_uint), ("info", ctypes.c_uint),
    ]


def drive_region(dev):
    """REPORT KEY, key class 0, key format 08h (RPC state)."""
    cdb = (ctypes.c_ubyte * 12)(0xA4, 0, 0, 0, 0, 0, 0, 0, 0, 8, 0x08, 0)
    data = (ctypes.c_ubyte * 8)()
    sense = (ctypes.c_ubyte * 32)()

    h = sg_io_hdr()
    h.interface_id = ord("S")
    h.dxfer_direction = SG_DXFER_FROM_DEV
    h.cmd_len, h.mx_sb_len, h.dxfer_len = 12, 32, 8
    h.dxferp = ctypes.cast(data, ctypes.c_void_p)
    h.cmdp = ctypes.cast(cdb, ctypes.c_void_p)
    h.sbp = ctypes.cast(sense, ctypes.c_void_p)
    h.timeout = 10000

    fd = os.open(dev, os.O_RDONLY | os.O_NONBLOCK)
    try:
        fcntl.ioctl(fd, SG_IO, h)
    finally:
        os.close(fd)

    if h.status != 0:
        raise OSError(f"REPORT KEY failed: scsi status {h.status}")
    return bytes(data)


def read_sectors(fd, lba, count=1):
    os.lseek(fd, lba * 2048, os.SEEK_SET)
    return os.read(fd, count * 2048)


def iso_entries(fd, lba, length):
    data = read_sectors(fd, lba, (length + 2047) // 2048)
    i = 0
    while i < len(data):
        rec_len = data[i]
        if rec_len == 0:  # padding to the end of the sector
            i = (i // 2048 + 1) * 2048
            if i >= len(data):
                return
            continue
        e = data[i:i + rec_len]
        name_len = e[32]
        name = e[33:33 + name_len].decode("latin1").split(";")[0]
        yield name, struct.unpack_from("<I", e, 2)[0], struct.unpack_from("<I", e, 10)[0]
        i += rec_len


def disc_region(dev):
    """The region mask from VIDEO_TS.IFO, or None if there is no DVD-Video."""
    fd = os.open(dev, os.O_RDONLY)
    try:
        pvd = read_sectors(fd, 16)
        if pvd[1:6] != b"CD001":
            return None
        root = pvd[156:156 + 34]
        ext = struct.unpack_from("<I", root, 2)[0]
        ln = struct.unpack_from("<I", root, 10)[0]

        vts = next((v for v in iso_entries(fd, ext, ln) if v[0].upper() == "VIDEO_TS"), None)
        if not vts:
            return None
        ifo = next((v for v in iso_entries(fd, vts[1], vts[2])
                    if v[0].upper() == "VIDEO_TS.IFO"), None)
        if not ifo:
            return None

        blk = read_sectors(fd, ifo[1])
        if blk[:12] != b"DVDVIDEO-VMG":
            return None
        # VMG_CATEGORY is a big-endian u32 at 0x22; the region mask is its
        # second byte. Reading 0x22 directly gets the wrong byte.
        return (struct.unpack_from(">I", blk, 0x22)[0] >> 16) & 0xFF
    finally:
        os.close(fd)


def regions(mask):
    return [i + 1 for i in range(8) if not (mask >> i) & 1]


def main():
    dev = sys.argv[1] if len(sys.argv) > 1 else "/dev/sr0"

    b = drive_region(dev)
    typ = (b[4] >> 6) & 0b11
    vra = (b[4] >> 3) & 0b111
    ucca = b[4] & 0b111
    mask, scheme = b[5], b[6]

    print(f"drive {dev}")
    print(f"  raw            : {b.hex(' ')}")
    print(f"  rpc scheme     : {scheme} ({'RPC-2, enforced in firmware' if scheme == 1 else 'RPC-1, not enforced'})")
    print(f"  state          : {RPC_TYPE.get(typ, typ)}")
    print(f"  region mask    : 0x{mask:02x}", end="")
    print("  (nothing playable)" if mask == 0xFF else
          "  (all regions)" if mask == 0x00 else
          f"  (plays region {', '.join(map(str, regions(mask)))})")
    print(f"  changes left   : {ucca} user, {vra} vendor")

    try:
        disc = disc_region(dev)
    except OSError as e:
        print(f"\ndisc: could not be read ({e})")
        return 0

    if disc is None:
        print("\ndisc: no DVD-Video structure (not a video DVD, or no disc)")
        return 0

    allowed = regions(disc)
    print(f"\ndisc")
    print(f"  region mask    : 0x{disc:02x}")
    print(f"  plays in       : {'all regions' if disc == 0x00 else 'region ' + ', '.join(map(str, allowed))}")

    if scheme == 1 and typ == 0 and disc != 0x00:
        print("\nThe drive has no region set and will not authenticate CSS.")
        print("This disc will scan correctly and stall during the rip.")
    elif scheme == 1 and typ in (1, 2, 3) and mask != 0xFF:
        if not (set(regions(mask)) & set(allowed)) and disc != 0x00:
            print("\nThe drive's region does not match this disc. It will stall during the rip.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
