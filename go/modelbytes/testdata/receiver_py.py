"""The Python receiver under the model bytes conformance suite; the same
operations and lines as receiver_c.c.

    python -I receiver_py.py MODULE_DIR OP ARG
"""
import importlib.util
import os
import sys


def main():
    folder, op, arg = sys.argv[1:4]
    spec = importlib.util.spec_from_file_location("oa_model_bytes", os.path.join(folder, "oa_model_bytes.py"))
    mbmod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mbmod)
    try:
        mb = mbmod.open(arg)
    except mbmod.ModelBytesError as e:
        print("refused", mbmod.NAMES[e.code], flush=True)
        return
    print("ok", mb.length, mb.sha256 or "-", int(mb.memory), flush=True)
    if op == "open":
        for off in (0, mb.length // 2, mb.length - 16 if mb.length > 16 else 0):
            n = min(16, mb.length - off)
            try:
                got = mb.read_at(off, n)
                print("probe", off, "ok", got.hex(), int(got == bytes(mb.view[off:off + n])))
            except mbmod.ModelBytesError as e:
                print("probe", off, mbmod.NAMES[e.code], "", 0)
        for name, off, n in (("range_end", mb.length - 1, 2), ("range_far", (1 << 64) - 1, 1), ("range_zero", mb.length, 0)):
            try:
                mb.read_at(off, n)
                print(name, "ok")
            except mbmod.ModelBytesError as e:
                print(name, mbmod.NAMES[e.code])
        mb.release()
        print("released", flush=True)
    elif op == "write":
        try:
            mb.view[0:1] = b"x"
            print("protect allowed")
        except TypeError:
            print("protect refused")
        if os.name == "nt":
            import ctypes
            from ctypes import wintypes
            k32 = ctypes.WinDLL("kernel32", use_last_error=True)
            k32.GetCurrentProcess.restype = wintypes.HANDLE
            k32.MapViewOfFile.restype = ctypes.c_void_p
            k32.MapViewOfFile.argtypes = [wintypes.HANDLE, wintypes.DWORD, wintypes.DWORD, wintypes.DWORD, ctypes.c_size_t]
            k32.DuplicateHandle.argtypes = [wintypes.HANDLE, wintypes.HANDLE, wintypes.HANDLE, ctypes.POINTER(wintypes.HANDLE),
                                            wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
            h = int(arg[3:].split("?")[0])
            cur, dup = k32.GetCurrentProcess(), wintypes.HANDLE()
            ok = k32.DuplicateHandle(cur, h, cur, ctypes.byref(dup), 2, False, 0)
            print("dup", "allowed" if ok else "refused")
            if ok:
                k32.CloseHandle(dup)
            w = k32.MapViewOfFile(h, 2, 0, 0, 0)  # FILE_MAP_WRITE
            print("mapwrite", "allowed" if w else "refused")
        else:
            import mmap
            fd = int(arg[3:].split("?")[0])
            try:
                w = os.open(f"/proc/self/fd/{fd}", os.O_RDWR)
            except OSError:
                print("dup refused")
                print("mapwrite refused")
            else:
                try:
                    os.pwrite(w, b"x", 0)
                    print("dup allowed")
                except OSError:
                    print("dup refused")
                try:
                    mmap.mmap(w, mmap.PAGESIZE, flags=mmap.MAP_SHARED, prot=mmap.PROT_READ | mmap.PROT_WRITE).close()
                    print("mapwrite allowed")
                except OSError:
                    print("mapwrite refused")
                os.close(w)
        mb.release()
    elif op == "hold":
        sys.stdin.read()
        mb.release()
        print("released", flush=True)
    elif op == "exit":
        sys.stdout.flush()
        os._exit(0)


main()
