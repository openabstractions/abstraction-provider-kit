# Model bytes receivers

An engine OA launches receives a model as a model bytes URI in the argument
where it takes a model path:

    fd:<N>?offset=<bytes>&length=<bytes>[&sha256=<64 hex>][&lease=<M>][&kind=memory]

The receiver checks the descriptor, maps the bytes read-only and returns the
mapping; `release` unmaps it and ends the lease. The rules are the provider
kit's KIT-MB1..KIT-MB6 (`../../CONTRACT.md`, "Model bytes"); what a fill, its
proof, its seal and the mapping mean is storage's (storage `CONTRACT.md`,
"Content transfer").

| File | What it is |
|---|---|
| `oa_model_bytes.h` | single-file C receiver; `#define OA_MODEL_BYTES_IMPLEMENTATION` in one translation unit |
| `oa_model_bytes.py` | standard-library Python receiver |
| `modelbytes.go`, `lease*.go` | the lender's URI and lease pipe |
| `conformance_*_test.go`, `testdata/` | the suite every receiver passes |

## Platforms

| Platform | Lent object | Suite |
|---|---|---|
| Windows | a sealed pagefile-backed section, or a read-only file handle | `TestConformance` passes for both receivers |
| Linux | a sealed `memfd`, or a read-only file descriptor | `TestConformancePOSIX` passes for both receivers under WSL (2026-11-12) |
| macOS | not designed beyond a read-only `shm_open` descriptor | not run: the macOS branch of both receivers is untested |

Run the suite with `py -3.12 scripts/gotest.py
openabstractions-flat/abstraction-provider-kit/go/modelbytes` on Windows,
and with `go test ./modelbytes` from the kit's `go` folder on Linux. The C
receiver is compiled with `gcc`, or `cl` on PATH; the Python receiver runs
under `py -3.12` or `python3`.
