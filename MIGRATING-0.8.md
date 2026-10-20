# Provider implementation migration in 0.8

The 0.8 source train moves shared engine execution out of inference and ships
the packing operator as a separate program. Inference's generated wire and
capability contracts keep their current shapes. All affected modules move
together through the existing lockstep release owner.

The published comparison baselines are inference Go v0.3.0 and provider-kit Go
v0.1.0. Their tags and sums remain unchanged. The paths below are 0.8 source;
their presence here supplies no claim that a 0.8 tag or installer is published.

| Former inference Go path | 0.8 owner |
|---|---|
| `providerhost` | provider-kit `go/providerhost` |
| `providerhost/enginefiles` | facade `go/programfiles` |
| `providerhost/enginelaunch` | provider-kit `go/enginelaunch` for engine socket/GPU policy |
| `enginelaunch.CleanEnvironment`, `Computed`, `RegistryValue`, `SystemRegistry`, `ErrEnvironment` | facade `go/supervision` |
| `enginelaunch.MaxLibraryPath`, `UTF16Len`, `LongestOpen` | facade `go/supervision` |
| `providerhost/devicelost`, `enginehttp`, `kokoro`, `llamacpp`, `rgbpng`, `sdcpp`, `videoenc`, `wavfile`, `whispercpp` | the same package names in provider-kit `go/` |
| `providerhost/modelfiles`, `modelpack`, `noreplace`, `npudecode` | the same package names in provider-kit `go/` |
| `go/examples/image-provider` | provider-kit `go/examples/image-provider` |

For example, a generic installed-file proof imports
`github.com/openabstractions/abstraction-facade/go/programfiles`; an engine
provider imports `github.com/openabstractions/abstraction-provider-kit/go/providerhost`.
The former `enginefiles` exported types and operations retain their behavior
under the `programfiles` package name. Persisted manifest/metadata formats and
trust decisions keep their existing bytes and meanings. There are no forwarding
packages under inference that import the kit.

Mandatory image directive, dimension and header admission has one pure owner
in inference `go/mediationcontent`. It applies before forwarding to an unknown
OpenAI-compatible upstream as before. Kit `sdcpp` reuses that owner, including
the same sentinel errors and reason words. Import ErrEngineDirective, ErrImageSize
and ErrRequestForm from inference go/mediationcontent; these three canonical
sentinels have no mutable re-export binding in the new kit package. Existing
errors.Is checks retain their exact values. Its process, launch and video policy
remain kit code. No optional configuration callback controls those checks.

## Standalone packing

Build `go/cmd/modelpack` from this source, or use the tool delivered by a qualified
0.8 package. From the public provider-kit checkout:

```text
go -C go build -o modelpack ./cmd/modelpack
modelpack --help
modelpack weights.safetensors weights.zip --rates bf16=7,f16=15,bf16fixed3=20,disk=6.5
modelpack info weights.zip
modelpack verify weights.zip
modelpack restore weights.zip restored.safetensors
modelpack measure weights.safetensors
```

No arguments print help. The operator preserves the existing argument forms,
refusals, digest checks, no-replace writes and deterministic archive behavior.
The runtime's old `models pack` command directs the operator to `modelpack`.
It executes no codec or hidden fallback. `npudecode` remains an optional kit
library; this migration installs no NPU toolchain and enables no device run.

The existing redist tools roster, Windows/POSIX payloads and source-pin owner
carry the standalone program. Its historical source roster references the
published kit module v0.1.0; `cmd/modelpack` is absent from that version. The
existing lockstep preparation owner projects the row and its module to v0.8.0
before candidate building. Program admission refuses a roster version that
differs from its prepared graph, and public-build admission checks the command
exists in the exact selected module archive before linking. The historical row
alone establishes no buildable or verified command. Actual release binaries,
signing, installation and publication retain their normal qualification.
