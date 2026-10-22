"""The OLE compound file (MS-CFB): the container every Office 97–2003 file is.

A compound file is a small filesystem: sectors chained by a FAT, a directory of
streams, and a mini-stream for anything under 4096 bytes chained by its own
mini-FAT. This reads that structure whole into memory — a document is read
once, not seeked — and hands back streams by name."""

from __future__ import annotations

import struct

MAGIC = b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"
END = 0xFFFFFFFE  # end of chain
FREE = 0xFFFFFFFF


class Compound:
    def __init__(self, data: bytes) -> None:
        if data[:8] != MAGIC:
            raise ValueError("файл не является документом Office 97–2003 (нет сигнатуры OLE)")
        self.data = data
        self.sector = 1 << struct.unpack_from("<H", data, 30)[0]
        self.mini_sector = 1 << struct.unpack_from("<H", data, 32)[0]
        fat_count = struct.unpack_from("<I", data, 44)[0]
        first_dir = struct.unpack_from("<I", data, 48)[0]
        self.mini_cutoff = struct.unpack_from("<I", data, 56)[0]
        first_minifat = struct.unpack_from("<I", data, 60)[0]
        minifat_count = struct.unpack_from("<I", data, 64)[0]
        first_difat = struct.unpack_from("<I", data, 68)[0]
        self.fat = self._fat(fat_count, first_difat)
        self.dir = self._directory(first_dir)
        root = self.dir[0]
        self.mini_stream = self._chain(root["start"], root["size"])
        self.minifat = self._table(self._chain(first_minifat, minifat_count * self.sector)) if minifat_count else []

    # ---- structure ----------------------------------------------------

    def _read_sector(self, n: int) -> bytes:
        off = (n + 1) * self.sector
        return self.data[off:off + self.sector]

    def _table(self, raw: bytes) -> list[int]:
        return list(struct.unpack_from("<%dI" % (len(raw) // 4), raw))

    def _fat(self, count: int, first_difat: int) -> list[int]:
        sectors = list(struct.unpack_from("<109I", self.data, 76))
        difat = first_difat
        while difat not in (END, FREE) and len(sectors) < count:
            raw = self._read_sector(difat)
            entries = self._table(raw)
            sectors += entries[:-1]
            difat = entries[-1]
        fat: list[int] = []
        for s in sectors[:count]:
            fat += self._table(self._read_sector(s))
        return fat

    def _chain(self, start: int, size: int) -> bytes:
        out = bytearray()
        sec, seen = start, set()
        while sec not in (END, FREE) and sec not in seen and len(out) < size:
            seen.add(sec)
            out += self._read_sector(sec)
            sec = self.fat[sec] if sec < len(self.fat) else END
        return bytes(out[:size])

    def _mini_chain(self, start: int, size: int) -> bytes:
        out = bytearray()
        sec, seen = start, set()
        while sec not in (END, FREE) and sec not in seen and len(out) < size:
            seen.add(sec)
            off = sec * self.mini_sector
            out += self.mini_stream[off:off + self.mini_sector]
            sec = self.minifat[sec] if sec < len(self.minifat) else END
        return bytes(out[:size])

    def _directory(self, first: int) -> list[dict]:
        raw = self._chain(first, len(self.data))
        out = []
        for off in range(0, len(raw) - 127, 128):
            e = raw[off:off + 128]
            n = struct.unpack_from("<H", e, 64)[0]
            kind = e[66]
            if kind == 0 or n < 2:
                continue
            out.append({
                "name": e[:n - 2].decode("utf-16-le", "replace"), "kind": kind,
                "start": struct.unpack_from("<I", e, 116)[0],
                "size": struct.unpack_from("<Q", e, 120)[0] & 0xFFFFFFFF,
            })
        return out

    # ---- api ------------------------------------------------------------

    def names(self) -> list[str]:
        return [d["name"] for d in self.dir if d["kind"] == 2]

    def stream(self, name: str) -> bytes:
        for d in self.dir:
            if d["kind"] == 2 and d["name"] == name:
                if d["size"] < self.mini_cutoff:
                    return self._mini_chain(d["start"], d["size"])
                return self._chain(d["start"], d["size"])
        raise KeyError(f"в файле нет потока {name!r}")
