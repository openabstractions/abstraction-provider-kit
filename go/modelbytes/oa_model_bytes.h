/* oa_model_bytes.h - the model bytes receiver, OpenAbstractions' reference
 * implementation (provider kit CONTRACT.md, "Model bytes", KIT-MB1..KIT-MB6).
 *
 * An engine OA launches receives a model's bytes as a model bytes URI in the
 * argument where it takes a model path today:
 *
 *     fd:<N>?offset=<bytes>&length=<bytes>[&sha256=<64 hex>][&lease=<M>][&kind=memory]
 *
 * N is a descriptor the engine holds: a HANDLE value on Windows, a file
 * descriptor elsewhere. lease names a pipe end whose closing tells OA the
 * engine holds no reference any more. kind=memory marks bytes OA filled into
 * memory and sealed. A string that does not start with "fd:" is a path.
 *
 * The receiver checks the descriptor's type, size and read-only state, and
 * for kind=memory its seal, maps offset..offset+length read-only and returns
 * the mapping. Every read through oa_model_bytes_read_at is bounds checked.
 *
 * Single file, no dependency beyond the platform's own headers. In exactly
 * one translation unit:
 *
 *     #define OA_MODEL_BYTES_IMPLEMENTATION
 *     #include "oa_model_bytes.h"
 *
 * Licence: MIT, as the engines that vendor it.
 */
/* The implementation needs POSIX.1-2008 (O_CLOEXEC, pread); a strict ISO C
 * build hides it unless a feature macro asks for it before the first system
 * header. */
#if defined(OA_MODEL_BYTES_IMPLEMENTATION) && !defined(_WIN32) && !defined(_POSIX_C_SOURCE) && \
    !defined(_GNU_SOURCE) && !defined(_DEFAULT_SOURCE) && !defined(_XOPEN_SOURCE)
#define _POSIX_C_SOURCE 200809L
#endif

#ifndef OA_MODEL_BYTES_H
#define OA_MODEL_BYTES_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

enum {
    OA_MB_OK = 0,
    OA_MB_EURI = 1,      /* malformed URI, unknown or repeated parameter */
    OA_MB_EHANDLE = 2,   /* the descriptor is not one this process holds, or of the wrong type */
    OA_MB_EWRITABLE = 3, /* the descriptor allows writing, or a memory object is not sealed */
    OA_MB_ESIZE = 4,     /* offset + length lies beyond the object */
    OA_MB_EMAP = 5,      /* the read-only mapping failed */
    OA_MB_ERANGE = 6,    /* a read outside 0..length */
    OA_MB_EPATH = 7      /* a plain path could not be opened or mapped */
};

typedef struct oa_model_bytes {
    const void *data;   /* first byte of the object, read-only */
    uint64_t length;
    uint8_t sha256[32]; /* the digest OA proved, when has_sha256 */
    int has_sha256;
    int memory;         /* kind=memory */
    /* private */
    void *view_;
    uint64_t view_length_;
    intptr_t handle_;
    intptr_t lease_;
    int has_lease_;
} oa_model_bytes;

/* 1 when arg is a model bytes URI, 0 when it is a path. */
int oa_model_bytes_is_uri(const char *arg);
/* Open a URI or a path. On failure *out is zeroed and nothing stays open
 * except the lease, which is closed too. Returns OA_MB_OK or an OA_MB_E code. */
int oa_model_bytes_open(const char *arg, oa_model_bytes *out);
/* Copy n bytes at offset into dst. OA_MB_ERANGE unless offset + n <= length. */
int oa_model_bytes_read_at(const oa_model_bytes *mb, void *dst, size_t n, uint64_t offset);
/* Unmap, close the descriptor and end the lease. Safe to call twice. */
void oa_model_bytes_release(oa_model_bytes *mb);
/* A short English name of a code. */
const char *oa_model_bytes_error(int code);

#ifdef __cplusplus
}
#endif

#ifdef OA_MODEL_BYTES_IMPLEMENTATION

#include <string.h>

#ifdef _WIN32
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#include <windows.h>
#else
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>
#if defined(__linux__) && !defined(F_GET_SEALS)
/* linux/fcntl.h's memfd seal commands, hidden by a strict POSIX build. */
#define F_GET_SEALS 1034
#define F_SEAL_SHRINK 0x0002
#define F_SEAL_GROW 0x0004
#define F_SEAL_WRITE 0x0008
#endif
#endif

const char *oa_model_bytes_error(int code) {
    switch (code) {
    case OA_MB_OK: return "ok";
    case OA_MB_EURI: return "uri";
    case OA_MB_EHANDLE: return "handle";
    case OA_MB_EWRITABLE: return "writable";
    case OA_MB_ESIZE: return "size";
    case OA_MB_EMAP: return "map";
    case OA_MB_ERANGE: return "range";
    case OA_MB_EPATH: return "path";
    }
    return "unknown";
}

int oa_model_bytes_is_uri(const char *arg) {
    return arg != NULL && arg[0] == 'f' && arg[1] == 'd' && arg[2] == ':';
}

/* oa_mb__u64 parses decimal digits [s, e) with no sign and no leading zero
 * except "0" itself, refusing overflow. */
static int oa_mb__u64(const char *s, const char *e, uint64_t *v) {
    uint64_t x = 0;
    if (s >= e || e - s > 20 || (s[0] == '0' && e - s > 1)) return 0;
    for (; s < e; s++) {
        if (*s < '0' || *s > '9') return 0;
        if (x > (UINT64_MAX - (uint64_t)(*s - '0')) / 10) return 0;
        x = x * 10 + (uint64_t)(*s - '0');
    }
    *v = x;
    return 1;
}

static int oa_mb__hex(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    return -1; /* upper case is refused: OA spells digests in lower case */
}

typedef struct oa_mb__uri {
    uint64_t fd, offset, length, lease;
    int has_offset, has_length, has_lease, has_sha, memory;
    uint8_t sha[32];
} oa_mb__uri;

static int oa_mb__parse(const char *arg, oa_mb__uri *u) {
    const char *p = arg + 3, *q, *end;
    int has_kind = 0;
    memset(u, 0, sizeof *u);
    q = strchr(p, '?');
    if (q == NULL || !oa_mb__u64(p, q, &u->fd)) return OA_MB_EURI;
    p = q + 1;
    end = p + strlen(p);
    while (p < end) {
        const char *amp = strchr(p, '&'), *eq;
        const char *stop = amp ? amp : end;
        size_t key;
        eq = memchr(p, '=', (size_t)(stop - p));
        if (eq == NULL) return OA_MB_EURI;
        key = (size_t)(eq - p);
        eq++;
        if (key == 6 && memcmp(p, "offset", 6) == 0) {
            if (u->has_offset || !oa_mb__u64(eq, stop, &u->offset)) return OA_MB_EURI;
            u->has_offset = 1;
        } else if (key == 6 && memcmp(p, "length", 6) == 0) {
            if (u->has_length || !oa_mb__u64(eq, stop, &u->length)) return OA_MB_EURI;
            u->has_length = 1;
        } else if (key == 5 && memcmp(p, "lease", 5) == 0) {
            if (u->has_lease || !oa_mb__u64(eq, stop, &u->lease)) return OA_MB_EURI;
            u->has_lease = 1;
        } else if (key == 6 && memcmp(p, "sha256", 6) == 0) {
            int i;
            if (u->has_sha || stop - eq != 64) return OA_MB_EURI;
            for (i = 0; i < 32; i++) {
                int hi = oa_mb__hex(eq[2 * i]), lo = oa_mb__hex(eq[2 * i + 1]);
                if (hi < 0 || lo < 0) return OA_MB_EURI;
                u->sha[i] = (uint8_t)(hi << 4 | lo);
            }
            u->has_sha = 1;
        } else if (key == 4 && memcmp(p, "kind", 4) == 0) {
            if (has_kind || stop - eq != 6 || memcmp(eq, "memory", 6) != 0) return OA_MB_EURI;
            has_kind = 1;
            u->memory = 1;
        } else {
            return OA_MB_EURI;
        }
        p = amp ? amp + 1 : end;
        if (amp && p == end) return OA_MB_EURI; /* a trailing '&' */
    }
    if (!u->has_offset || !u->has_length || u->length == 0) return OA_MB_EURI;
    if (u->offset > UINT64_MAX - u->length) return OA_MB_EURI;
    return OA_MB_OK;
}

#ifdef _WIN32

/* ntdll's object and section queries, found at run time. */
typedef LONG(NTAPI *oa_mb__query_object)(HANDLE, int, PVOID, ULONG, PULONG);
typedef LONG(NTAPI *oa_mb__query_section)(HANDLE, int, PVOID, SIZE_T, PSIZE_T);

typedef struct {
    ULONG Attributes;
    ACCESS_MASK GrantedAccess;
    ULONG HandleCount, PointerCount, Reserved[10];
} oa_mb__basic;

typedef struct {
    USHORT Length, MaximumLength;
    PWSTR Buffer;
} oa_mb__unicode;

typedef struct {
    PVOID BaseAddress;
    ULONG AllocationAttributes;
    LARGE_INTEGER MaximumSize;
} oa_mb__section_basic;

#define OA_MB_SECTION_QUERY 0x0001
#define OA_MB_SECTION_MAP_WRITE 0x0002
#define OA_MB_SECTION_MAP_READ 0x0004
#define OA_MB_SECTION_MAP_EXECUTE 0x0008
#define OA_MB_SECTION_EXTEND_SIZE 0x0010
#define OA_MB_SEC_FILE 0x00800000

/* oa_mb__type reads the kernel object type of h: 1 a section, 2 a file, 0
 * anything else or no object at all. */
static int oa_mb__type(HANDLE h, ACCESS_MASK *granted) {
    HMODULE nt = GetModuleHandleW(L"ntdll.dll");
    oa_mb__query_object query = nt ? (oa_mb__query_object)(void (*)(void))GetProcAddress(nt, "NtQueryObject") : NULL;
    union { oa_mb__unicode name; BYTE raw[1024]; } type;
    oa_mb__basic basic;
    ULONG got = 0;
    if (query == NULL) return 0;
    if (query(h, 0 /* ObjectBasicInformation */, &basic, sizeof basic, &got) < 0) return 0;
    *granted = basic.GrantedAccess;
    if (query(h, 2 /* ObjectTypeInformation */, &type, sizeof type, &got) < 0) return 0;
    if (type.name.Length == 7 * sizeof(WCHAR) && memcmp(type.name.Buffer, L"Section", 7 * sizeof(WCHAR)) == 0) return 1;
    if (type.name.Length == 4 * sizeof(WCHAR) && memcmp(type.name.Buffer, L"File", 4 * sizeof(WCHAR)) == 0) return 2;
    return 0;
}

/* oa_mb__raisable reports whether any handle to h's object could be opened
 * for writing from this process: the seal OA's protected descriptor gives. */
static int oa_mb__raisable(HANDLE h, DWORD access) {
    HANDLE dup = NULL;
    if (!DuplicateHandle(GetCurrentProcess(), h, GetCurrentProcess(), &dup, access, FALSE, 0)) return 0;
    CloseHandle(dup);
    return 1;
}

static int oa_mb__open_handle(const oa_mb__uri *u, oa_model_bytes *mb) {
    HANDLE h = (HANDLE)(uintptr_t)u->fd, mapping = NULL;
    ACCESS_MASK granted = 0;
    uint64_t size, start;
    SYSTEM_INFO si;
    void *view;
    int type = oa_mb__type(h, &granted);
    if (type == 0) return OA_MB_EHANDLE;
    /* The engine's own children never receive it. */
    SetHandleInformation(h, HANDLE_FLAG_INHERIT, 0);
    if (type == 1) {
        HMODULE nt = GetModuleHandleW(L"ntdll.dll");
        oa_mb__query_section query = (oa_mb__query_section)(void (*)(void))GetProcAddress(nt, "NtQuerySection");
        oa_mb__section_basic sb;
        SIZE_T got = 0;
        if (granted & (OA_MB_SECTION_MAP_WRITE | OA_MB_SECTION_MAP_EXECUTE | OA_MB_SECTION_EXTEND_SIZE | WRITE_DAC | WRITE_OWNER))
            return OA_MB_EWRITABLE;
        if (!(granted & OA_MB_SECTION_MAP_READ) || !(granted & OA_MB_SECTION_QUERY)) return OA_MB_EHANDLE;
        if (query == NULL || query(h, 0 /* SectionBasicInformation */, &sb, sizeof sb, &got) < 0) return OA_MB_EHANDLE;
        /* kind=memory is a pagefile-backed section; any other is file-backed. */
        if (u->memory == ((sb.AllocationAttributes & OA_MB_SEC_FILE) != 0)) return OA_MB_EHANDLE;
        if (oa_mb__raisable(h, OA_MB_SECTION_MAP_WRITE) || oa_mb__raisable(h, WRITE_DAC)) return OA_MB_EWRITABLE;
        size = (uint64_t)sb.MaximumSize.QuadPart;
    } else {
        LARGE_INTEGER fs;
        if (u->memory) return OA_MB_EHANDLE;
        if (granted & (FILE_WRITE_DATA | FILE_APPEND_DATA | WRITE_DAC | WRITE_OWNER | DELETE)) return OA_MB_EWRITABLE;
        if (!GetFileSizeEx(h, &fs)) return OA_MB_EHANDLE;
        size = (uint64_t)fs.QuadPart;
    }
    if (u->offset + u->length > size) return OA_MB_ESIZE;
    if (type == 2) {
        mapping = CreateFileMappingW(h, NULL, PAGE_READONLY, 0, 0, NULL);
        if (mapping == NULL) return OA_MB_EMAP;
    }
    GetSystemInfo(&si);
    start = u->offset - u->offset % si.dwAllocationGranularity;
    view = MapViewOfFile(mapping ? mapping : h, FILE_MAP_READ, (DWORD)(start >> 32), (DWORD)start,
                         (SIZE_T)(u->offset + u->length - start));
    if (mapping) CloseHandle(mapping);
    if (view == NULL) return OA_MB_EMAP;
    mb->view_ = view;
    mb->view_length_ = u->offset + u->length - start;
    mb->data = (const uint8_t *)view + (u->offset - start);
    mb->handle_ = (intptr_t)h;
    return OA_MB_OK;
}

static int oa_mb__open_path(const char *path, oa_model_bytes *mb) {
    int n = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, NULL, 0);
    WCHAR *w;
    HANDLE f, mapping;
    LARGE_INTEGER fs;
    void *view;
    if (n <= 0 || (w = (WCHAR *)HeapAlloc(GetProcessHeap(), 0, (SIZE_T)n * sizeof(WCHAR))) == NULL) return OA_MB_EPATH;
    MultiByteToWideChar(CP_UTF8, 0, path, -1, w, n);
    f = CreateFileW(w, GENERIC_READ, FILE_SHARE_READ, NULL, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
    HeapFree(GetProcessHeap(), 0, w);
    if (f == INVALID_HANDLE_VALUE) return OA_MB_EPATH;
    if (!GetFileSizeEx(f, &fs) || fs.QuadPart <= 0) { CloseHandle(f); return OA_MB_EPATH; }
    mapping = CreateFileMappingW(f, NULL, PAGE_READONLY, 0, 0, NULL);
    CloseHandle(f);
    if (mapping == NULL) return OA_MB_EPATH;
    view = MapViewOfFile(mapping, FILE_MAP_READ, 0, 0, 0);
    CloseHandle(mapping);
    if (view == NULL) return OA_MB_EPATH;
    mb->view_ = view;
    mb->view_length_ = (uint64_t)fs.QuadPart;
    mb->data = view;
    mb->length = (uint64_t)fs.QuadPart;
    mb->handle_ = 0;
    return OA_MB_OK;
}

static void oa_mb__close_lease(intptr_t lease) {
    CloseHandle((HANDLE)lease);
}

void oa_model_bytes_release(oa_model_bytes *mb) {
    if (mb == NULL) return;
    if (mb->view_) UnmapViewOfFile(mb->view_);
    if (mb->handle_) CloseHandle((HANDLE)mb->handle_);
    if (mb->has_lease_) oa_mb__close_lease(mb->lease_);
    memset(mb, 0, sizeof *mb);
}

#else /* POSIX */

static int oa_mb__open_handle(const oa_mb__uri *u, oa_model_bytes *mb) {
    int fd = (int)u->fd, flags;
    struct stat st;
    long page = sysconf(_SC_PAGESIZE);
    uint64_t start;
    void *view;
    if (u->fd > 0x7fffffff || fstat(fd, &st) != 0) return OA_MB_EHANDLE;
    fcntl(fd, F_SETFD, FD_CLOEXEC);
    if (!S_ISREG(st.st_mode)) return OA_MB_EHANDLE;
    flags = fcntl(fd, F_GETFL);
    if (flags < 0) return OA_MB_EHANDLE;
    if ((flags & O_ACCMODE) != O_RDONLY) return OA_MB_EWRITABLE;
    if (u->memory) {
#if defined(__linux__) && defined(F_GET_SEALS)
        char link[64], target[16];
        ssize_t got;
        int seals = fcntl(fd, F_GET_SEALS);
        int need = F_SEAL_WRITE | F_SEAL_GROW | F_SEAL_SHRINK;
        /* A memory object is a memfd; a file of a file system is not. */
        snprintf(link, sizeof link, "/proc/self/fd/%d", fd);
        got = readlink(link, target, sizeof target);
        if (got < 7 || memcmp(target, "/memfd:", 7) != 0 || seals < 0) return OA_MB_EHANDLE;
        if ((seals & need) != need) return OA_MB_EWRITABLE;
#else
        return OA_MB_EHANDLE; /* no sealed memory object here */
#endif
    }
    if (u->offset + u->length > (uint64_t)st.st_size) return OA_MB_ESIZE;
    start = u->offset - u->offset % (uint64_t)page;
    view = mmap(NULL, (size_t)(u->offset + u->length - start), PROT_READ, MAP_SHARED, fd, (off_t)start);
    if (view == MAP_FAILED) return OA_MB_EMAP;
    mb->view_ = view;
    mb->view_length_ = u->offset + u->length - start;
    mb->data = (const uint8_t *)view + (u->offset - start);
    mb->handle_ = fd;
    return OA_MB_OK;
}

static int oa_mb__open_path(const char *path, oa_model_bytes *mb) {
    struct stat st;
    void *view;
    int fd = open(path, O_RDONLY | O_CLOEXEC);
    if (fd < 0) return OA_MB_EPATH;
    if (fstat(fd, &st) != 0 || !S_ISREG(st.st_mode) || st.st_size <= 0) { close(fd); return OA_MB_EPATH; }
    view = mmap(NULL, (size_t)st.st_size, PROT_READ, MAP_SHARED, fd, 0);
    close(fd);
    if (view == MAP_FAILED) return OA_MB_EPATH;
    mb->view_ = view;
    mb->view_length_ = (uint64_t)st.st_size;
    mb->data = view;
    mb->length = (uint64_t)st.st_size;
    mb->handle_ = -1;
    return OA_MB_OK;
}

static void oa_mb__close_lease(intptr_t lease) {
    close((int)lease);
}

void oa_model_bytes_release(oa_model_bytes *mb) {
    if (mb == NULL) return;
    if (mb->view_) munmap(mb->view_, (size_t)mb->view_length_);
    if (mb->handle_ > 0) close((int)mb->handle_);
    if (mb->has_lease_) oa_mb__close_lease(mb->lease_);
    memset(mb, 0, sizeof *mb);
}

#endif

int oa_model_bytes_open(const char *arg, oa_model_bytes *out) {
    oa_mb__uri u;
    int rc;
    if (out == NULL || arg == NULL) return OA_MB_EURI;
    memset(out, 0, sizeof *out);
    if (!oa_model_bytes_is_uri(arg)) return oa_mb__open_path(arg, out);
    rc = oa_mb__parse(arg, &u);
    if (rc != OA_MB_OK) return rc;
    if (u.has_lease) {
        /* The lease is the engine's from here: it ends with release, with a
         * refused open, or with the engine's exit. */
        out->lease_ = (intptr_t)u.lease;
        out->has_lease_ = 1;
#ifdef _WIN32
        SetHandleInformation((HANDLE)(uintptr_t)u.lease, HANDLE_FLAG_INHERIT, 0);
#else
        if (u.lease <= 0x7fffffff) fcntl((int)u.lease, F_SETFD, FD_CLOEXEC);
#endif
    }
    rc = oa_mb__open_handle(&u, out);
    if (rc != OA_MB_OK) {
        int had = out->has_lease_;
        intptr_t lease = out->lease_;
        memset(out, 0, sizeof *out);
        if (had) oa_mb__close_lease(lease);
        return rc;
    }
    out->length = u.length;
    out->memory = u.memory;
    out->has_sha256 = u.has_sha;
    memcpy(out->sha256, u.sha, sizeof u.sha);
    return OA_MB_OK;
}

int oa_model_bytes_read_at(const oa_model_bytes *mb, void *dst, size_t n, uint64_t offset) {
    if (mb == NULL || mb->data == NULL) return OA_MB_ERANGE;
    if (offset > mb->length || (uint64_t)n > mb->length - offset) return OA_MB_ERANGE;
    if (n) memcpy(dst, (const uint8_t *)mb->data + offset, n);
    return OA_MB_OK;
}

#endif /* OA_MODEL_BYTES_IMPLEMENTATION */
#endif /* OA_MODEL_BYTES_H */
