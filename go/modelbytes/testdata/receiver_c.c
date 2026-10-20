/* The C receiver under the model bytes conformance suite: one operation on
 * one argument, answered in lines the suite reads (conformance_windows_test.go).
 *
 *   receiver_c open ARG    open, report length, digest and probes, bounds, release
 *   receiver_c write ARG   open, then try to write the bytes three ways
 *   receiver_c hold ARG    open, wait for stdin to close, release
 *   receiver_c exit ARG    open, then exit without releasing
 */
#define OA_MODEL_BYTES_IMPLEMENTATION
#include "oa_model_bytes.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#ifdef _WIN32
#include <windows.h>
#else
#include <fcntl.h>
#include <sys/mman.h>
#include <unistd.h>
#endif

static void hex(const uint8_t *b, size_t n) {
    size_t i;
    for (i = 0; i < n; i++) printf("%02x", b[i]);
}

static void probe(const oa_model_bytes *mb, uint64_t off) {
    uint8_t buf[16];
    size_t n = mb->length - off < 16 ? (size_t)(mb->length - off) : 16;
    int rc = oa_model_bytes_read_at(mb, buf, n, off);
    printf("probe %llu %s ", (unsigned long long)off, oa_model_bytes_error(rc));
    hex(buf, rc == OA_MB_OK ? n : 0);
    printf(" %d\n", rc == OA_MB_OK && memcmp(buf, (const uint8_t *)mb->data + off, n) == 0);
}

int main(int argc, char **argv) {
    oa_model_bytes mb;
    const char *op, *arg;
    int rc;
    if (argc != 3) return 2;
    op = argv[1];
    arg = argv[2];
    rc = oa_model_bytes_open(arg, &mb);
    if (rc != OA_MB_OK) {
        printf("refused %s\n", oa_model_bytes_error(rc));
        return 0;
    }
    printf("ok %llu ", (unsigned long long)mb.length);
    if (mb.has_sha256) hex(mb.sha256, 32); else printf("-");
    printf(" %d\n", mb.memory);
    fflush(stdout);
    if (strcmp(op, "open") == 0) {
        uint8_t one[2];
        probe(&mb, 0);
        probe(&mb, mb.length / 2);
        probe(&mb, mb.length > 16 ? mb.length - 16 : 0);
        printf("range_end %s\n", oa_model_bytes_error(oa_model_bytes_read_at(&mb, one, 2, mb.length - 1)));
        printf("range_far %s\n", oa_model_bytes_error(oa_model_bytes_read_at(&mb, one, 1, UINT64_MAX)));
        printf("range_zero %s\n", oa_model_bytes_error(oa_model_bytes_read_at(&mb, one, 0, mb.length)));
        oa_model_bytes_release(&mb);
        printf("released\n");
    } else if (strcmp(op, "write") == 0) {
#ifdef _WIN32
        DWORD old;
        HANDLE dup = NULL, h = (HANDLE)(uintptr_t)strtoull(arg + 3, NULL, 10);
        void *w;
        printf("protect %s\n", VirtualProtect((LPVOID)mb.data, 1, PAGE_READWRITE, &old) ? "allowed" : "refused");
        if (DuplicateHandle(GetCurrentProcess(), h, GetCurrentProcess(), &dup, 0x0002 /* SECTION_MAP_WRITE */, FALSE, 0)) {
            printf("dup allowed\n");
            CloseHandle(dup);
        } else {
            printf("dup refused\n");
        }
        w = MapViewOfFile(h, FILE_MAP_WRITE, 0, 0, 0);
        printf("mapwrite %s\n", w ? "allowed" : "refused");
        if (w) UnmapViewOfFile(w);
#else
        long page = sysconf(_SC_PAGESIZE);
        int fd = atoi(arg + 3), w;
        char path[64];
        void *m;
        printf("protect %s\n", mprotect((void *)((uintptr_t)mb.data & ~(uintptr_t)(page - 1)), (size_t)page,
                                        PROT_READ | PROT_WRITE) == 0 ? "allowed" : "refused");
        snprintf(path, sizeof path, "/proc/self/fd/%d", fd);
        w = open(path, O_RDWR);
        if (w < 0) {
            printf("dup refused\nmapwrite refused\n");
        } else {
            printf("dup %s\n", pwrite(w, "x", 1, 0) == 1 ? "allowed" : "refused");
            m = mmap(NULL, (size_t)page, PROT_READ | PROT_WRITE, MAP_SHARED, w, 0);
            printf("mapwrite %s\n", m != MAP_FAILED ? "allowed" : "refused");
            if (m != MAP_FAILED) munmap(m, (size_t)page);
            close(w);
        }
#endif
        oa_model_bytes_release(&mb);
    } else if (strcmp(op, "hold") == 0) {
        while (getchar() != EOF) {
        }
        oa_model_bytes_release(&mb);
        printf("released\n");
    } else if (strcmp(op, "exit") == 0) {
        fflush(stdout);
        _Exit(0);
    }
    fflush(stdout);
    return 0;
}
