"""The model bytes receiver for Python engines (provider kit CONTRACT.md,
"Model bytes", KIT-MB1..KIT-MB6); the same contract as oa_model_bytes.h.

An engine OA launches receives a model's bytes as a model bytes URI where it
takes a model path today:

    fd:<N>?offset=<bytes>&length=<bytes>[&sha256=<64 hex>][&lease=<M>][&kind=memory]

open(arg) checks the descriptor's type, size and read-only state, and for
kind=memory its seal, maps offset..offset+length read-only and returns a
ModelBytes whose view is a read-only memoryview of the object. A string that
does not start with "fd:" is a path, mapped read-only the same way.

Standard library only. On Windows the standard mmap module cannot map a
section handle, so the receiver calls MapViewOfFile through ctypes.
"""
import io
import os
import re

OK, EURI, EHANDLE, EWRITABLE, ESIZE, EMAP, ERANGE, EPATH = range(8)
NAMES = ["ok", "uri", "handle", "writable", "size", "map", "range", "path"]

_NUMBER = r"(0|[1-9][0-9]{0,19})"
_PARAM = re.compile(r"(offset|length|lease)=" + _NUMBER + r"|sha256=([0-9a-f]{64})|kind=(memory)")


class ModelBytesError(Exception):
    """A refused open or read. code is one of the module's E codes."""

    def __init__(self, code, detail=""):
        super().__init__(f"model bytes: {NAMES[code]}" + (f": {detail}" if detail else ""))
        self.code = code


def is_uri(arg):
    return isinstance(arg, str) and arg.startswith("fd:")


def parse(arg):
    """The URI's fields as a dict, or ModelBytesError(EURI)."""
    head, sep, query = arg[3:].partition("?")
    if not sep or re.fullmatch(_NUMBER, head) is None:
        raise ModelBytesError(EURI, "descriptor")
    out = {"fd": int(head), "memory": False, "sha256": None, "lease": None}
    seen = set()
    for part in query.split("&"):
        m = _PARAM.fullmatch(part)
        key = part.partition("=")[0]
        if m is None or key in seen:
            raise ModelBytesError(EURI, part)
        seen.add(key)
        if m.group(1):
            out[m.group(1)] = int(m.group(2))
        elif m.group(3):
            out["sha256"] = m.group(3)
        else:
            out["memory"] = True
    if "offset" not in seen or "length" not in seen or out["length"] == 0:
        raise ModelBytesError(EURI, "offset and length")
    if out["offset"] + out["length"] > (1 << 64) - 1 or out["fd"] > (1 << 64) - 1:
        raise ModelBytesError(EURI, "range")
    return out


class ModelBytes:
    """One opened object: view is a read-only memoryview of its length
    bytes. release() unmaps, closes the descriptor and ends the lease; it
    raises BufferError while tensors or slices still use the view."""

    def __init__(self):
        self.view, self.length, self.sha256, self.memory = None, 0, None, False
        self._keep, self._unmap, self._close = None, None, []

    def read_at(self, offset, n):
        if self.view is None or offset < 0 or n < 0 or offset > self.length or n > self.length - offset:
            raise ModelBytesError(ERANGE, f"{n} bytes at {offset} of {self.length}")
        return bytes(self.view[offset:offset + n])

    def release(self):
        if self.view is not None:
            self.view.release()
            self.view = None
        self._keep = None
        if self._unmap is not None:
            self._unmap()
            self._unmap = None
        for close in self._close:
            close()
        self._close = []

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.release()


if os.name == "nt":
    import ctypes
    from ctypes import wintypes

    _k32 = ctypes.WinDLL("kernel32", use_last_error=True)
    _nt = ctypes.WinDLL("ntdll")
    _k32.MapViewOfFile.restype = ctypes.c_void_p
    _k32.MapViewOfFile.argtypes = [wintypes.HANDLE, wintypes.DWORD, wintypes.DWORD, wintypes.DWORD, ctypes.c_size_t]
    _k32.UnmapViewOfFile.argtypes = [ctypes.c_void_p]
    _k32.CloseHandle.argtypes = [wintypes.HANDLE]
    _k32.SetHandleInformation.argtypes = [wintypes.HANDLE, wintypes.DWORD, wintypes.DWORD]
    _k32.GetCurrentProcess.restype = wintypes.HANDLE
    _k32.DuplicateHandle.argtypes = [wintypes.HANDLE, wintypes.HANDLE, wintypes.HANDLE, ctypes.POINTER(wintypes.HANDLE),
                                     wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    _k32.GetFileSizeEx.argtypes = [wintypes.HANDLE, ctypes.POINTER(ctypes.c_longlong)]
    _k32.CreateFileMappingW.restype = wintypes.HANDLE
    _k32.CreateFileMappingW.argtypes = [wintypes.HANDLE, ctypes.c_void_p, wintypes.DWORD, wintypes.DWORD, wintypes.DWORD,
                                        wintypes.LPCWSTR]
    _nt.NtQueryObject.restype = ctypes.c_long
    _nt.NtQueryObject.argtypes = [wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p, wintypes.ULONG, ctypes.POINTER(wintypes.ULONG)]
    _nt.NtQuerySection.restype = ctypes.c_long
    _nt.NtQuerySection.argtypes = [wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p, ctypes.c_size_t, ctypes.POINTER(ctypes.c_size_t)]

    SECTION_QUERY, SECTION_MAP_WRITE, SECTION_MAP_READ, SECTION_MAP_EXECUTE, SECTION_EXTEND_SIZE = 1, 2, 4, 8, 0x10
    WRITE_DAC, WRITE_OWNER, DELETE = 0x40000, 0x80000, 0x10000
    FILE_WRITE_DATA, FILE_APPEND_DATA = 2, 4
    SEC_FILE, FILE_MAP_READ, PAGE_READONLY = 0x800000, 4, 2

    class _Basic(ctypes.Structure):
        _fields_ = [("Attributes", wintypes.ULONG), ("GrantedAccess", wintypes.DWORD), ("HandleCount", wintypes.ULONG),
                    ("PointerCount", wintypes.ULONG), ("Reserved", wintypes.ULONG * 10)]

    class _SectionBasic(ctypes.Structure):
        _fields_ = [("BaseAddress", ctypes.c_void_p), ("AllocationAttributes", wintypes.ULONG),
                    ("MaximumSize", ctypes.c_longlong)]

    class _Unicode(ctypes.Structure):
        _fields_ = [("Length", wintypes.USHORT), ("MaximumLength", wintypes.USHORT), ("Buffer", ctypes.c_void_p)]

    def _type(h):
        """("section"|"file"|None, granted access) of handle h."""
        basic, got = _Basic(), wintypes.ULONG()
        if _nt.NtQueryObject(h, 0, ctypes.byref(basic), ctypes.sizeof(basic), ctypes.byref(got)) < 0:
            return None, 0
        raw = (ctypes.c_ubyte * 1024)()
        if _nt.NtQueryObject(h, 2, raw, 1024, ctypes.byref(got)) < 0:
            return None, 0
        name = _Unicode.from_buffer(raw)
        word = ctypes.wstring_at(name.Buffer, name.Length // 2) if name.Buffer else ""
        return {"Section": "section", "File": "file"}.get(word), basic.GrantedAccess

    def _raisable(h, access):
        cur, dup = _k32.GetCurrentProcess(), wintypes.HANDLE()
        if not _k32.DuplicateHandle(cur, h, cur, ctypes.byref(dup), access, False, 0):
            return False
        _k32.CloseHandle(dup)
        return True

    def _map(h, start, size, mb, offset):
        addr = _k32.MapViewOfFile(h, FILE_MAP_READ, start >> 32, start & 0xFFFFFFFF, size)
        if not addr:
            return False
        arr = (ctypes.c_ubyte * size).from_address(addr)
        mb._keep = arr
        mb.view = memoryview(arr).cast("B")[offset:].toreadonly()
        mb._unmap = lambda: _k32.UnmapViewOfFile(addr)
        return True

    def _open_handle(u, mb):
        h = u["fd"]
        kind, granted = _type(h)
        if kind is None:
            raise ModelBytesError(EHANDLE, "no section or file handle")
        _k32.SetHandleInformation(h, 1, 0)  # HANDLE_FLAG_INHERIT off
        if kind == "section":
            if granted & (SECTION_MAP_WRITE | SECTION_MAP_EXECUTE | SECTION_EXTEND_SIZE | WRITE_DAC | WRITE_OWNER):
                raise ModelBytesError(EWRITABLE, "the handle maps for writing")
            if not granted & SECTION_MAP_READ or not granted & SECTION_QUERY:
                raise ModelBytesError(EHANDLE, "the handle cannot map for reading")
            sb, got = _SectionBasic(), ctypes.c_size_t()
            if _nt.NtQuerySection(h, 0, ctypes.byref(sb), ctypes.sizeof(sb), ctypes.byref(got)) < 0:
                raise ModelBytesError(EHANDLE, "section query")
            if u["memory"] == bool(sb.AllocationAttributes & SEC_FILE):
                raise ModelBytesError(EHANDLE, "the section's backing differs from the URI's kind")
            if _raisable(h, SECTION_MAP_WRITE) or _raisable(h, WRITE_DAC):
                raise ModelBytesError(EWRITABLE, "the section is not sealed")
            size, mapping = sb.MaximumSize, h
        else:
            if u["memory"]:
                raise ModelBytesError(EHANDLE, "kind=memory names a section")
            if granted & (FILE_WRITE_DATA | FILE_APPEND_DATA | WRITE_DAC | WRITE_OWNER | DELETE):
                raise ModelBytesError(EWRITABLE, "the file handle allows writing")
            size = ctypes.c_longlong()
            if not _k32.GetFileSizeEx(h, ctypes.byref(size)):
                raise ModelBytesError(EHANDLE, "file size")
            size = size.value
            mapping = None
        if u["offset"] + u["length"] > size:
            raise ModelBytesError(ESIZE, f"{u['length']} bytes at {u['offset']} of {size}")
        made = None
        if mapping is None:
            made = _k32.CreateFileMappingW(h, None, PAGE_READONLY, 0, 0, None)
            if not made:
                raise ModelBytesError(EMAP, "file mapping")
            mapping = made
        start = u["offset"] - u["offset"] % (64 << 10)
        try:
            if not _map(mapping, start, u["offset"] + u["length"] - start, mb, u["offset"] - start):
                raise ModelBytesError(EMAP, f"MapViewOfFile: {ctypes.get_last_error()}")
        finally:
            if made:
                _k32.CloseHandle(made)
        mb._close.append(lambda: _k32.CloseHandle(h))

    def _open_path(path, mb):
        import mmap
        try:
            with io.open(path, "rb") as f:
                size = os.fstat(f.fileno()).st_size
                if size <= 0:
                    raise ModelBytesError(EPATH, "empty")
                mm = mmap.mmap(f.fileno(), size, access=mmap.ACCESS_READ)
        except OSError as e:
            raise ModelBytesError(EPATH, str(e)) from None
        mb._keep, mb.view, mb.length = mm, memoryview(mm), size
        mb._unmap = mm.close

    def _close_lease(lease):
        _k32.CloseHandle(lease)

    def _own_lease(lease):
        _k32.SetHandleInformation(lease, 1, 0)

else:
    import fcntl
    import mmap
    import stat

    def _open_handle(u, mb):
        fd = u["fd"]
        try:
            st = os.fstat(fd)
            os.set_inheritable(fd, False)
            flags = fcntl.fcntl(fd, fcntl.F_GETFL)
        except (OSError, OverflowError):
            raise ModelBytesError(EHANDLE, "no descriptor") from None
        if not stat.S_ISREG(st.st_mode):
            raise ModelBytesError(EHANDLE, "not a regular file or memory object")
        if flags & os.O_ACCMODE != os.O_RDONLY:
            raise ModelBytesError(EWRITABLE, "the descriptor is open for writing")
        if u["memory"]:
            if not hasattr(fcntl, "F_GET_SEALS"):
                raise ModelBytesError(EHANDLE, "no sealed memory object on this platform")
            need = fcntl.F_SEAL_WRITE | fcntl.F_SEAL_GROW | fcntl.F_SEAL_SHRINK
            try:
                if not os.readlink(f"/proc/self/fd/{fd}").startswith("/memfd:"):
                    raise ModelBytesError(EHANDLE, "kind=memory names a memfd")
            except OSError:
                raise ModelBytesError(EHANDLE, "kind=memory names a memfd") from None
            try:
                seals = fcntl.fcntl(fd, fcntl.F_GET_SEALS)
            except OSError:
                raise ModelBytesError(EHANDLE, "no sealable memory object") from None
            if seals & need != need:
                raise ModelBytesError(EWRITABLE, "the memory object is not sealed")
        if u["offset"] + u["length"] > st.st_size:
            raise ModelBytesError(ESIZE, f"{u['length']} bytes at {u['offset']} of {st.st_size}")
        start = u["offset"] - u["offset"] % mmap.ALLOCATIONGRANULARITY
        try:
            mm = mmap.mmap(fd, u["offset"] + u["length"] - start, prot=mmap.PROT_READ, flags=mmap.MAP_SHARED, offset=start)
        except OSError as e:
            raise ModelBytesError(EMAP, str(e)) from None
        mb._keep, mb.view = mm, memoryview(mm)[u["offset"] - start:]
        mb._unmap = mm.close
        mb._close.append(lambda: os.close(fd))

    def _open_path(path, mb):
        try:
            with io.open(path, "rb") as f:
                size = os.fstat(f.fileno()).st_size
                if size <= 0:
                    raise ModelBytesError(EPATH, "empty")
                mm = mmap.mmap(f.fileno(), size, prot=mmap.PROT_READ, flags=mmap.MAP_SHARED)
        except OSError as e:
            raise ModelBytesError(EPATH, str(e)) from None
        mb._keep, mb.view, mb.length = mm, memoryview(mm), size
        mb._unmap = mm.close

    def _close_lease(lease):
        try:
            os.close(lease)
        except OSError:
            pass

    def _own_lease(lease):
        try:
            os.set_inheritable(lease, False)
        except OSError:
            pass


def open(arg):  # noqa: A001 - the receiver's one entry point, as in the C header
    """The bytes arg names: a model bytes URI or a path. A refused open
    closes the lease too."""
    mb = ModelBytes()
    if not is_uri(arg):
        _open_path(arg, mb)
        return mb
    u = parse(arg)
    if u["lease"] is not None:
        _own_lease(u["lease"])
        mb._close.append(lambda: _close_lease(u["lease"]))
    try:
        _open_handle(u, mb)
    except BaseException:
        mb.release()
        raise
    mb.length, mb.sha256, mb.memory = u["length"], u["sha256"], u["memory"]
    return mb
