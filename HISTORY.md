# History

Design records for [`CONTRACT.md`](CONTRACT.md).

## 2026-10-03: the engine's sandbox

The owner rejected the PyTorch provider's 30-second socket check as
containment (2026-10-02) and asked for Windows' own sandboxing. The research
(`research/engine-sandbox/REPORT.md`) measured a capability-less
AppContainer blocking TCP, DNS and UDP, children included, with no
administrator, and the GPU test of 2026-10-03 ran the HIP device query and a
kernel inside one. LPAC failed there: `import torch` reaches asyncio, which
calls `socket()`.

The coordinator put the sandbox here because every engine OA launches will
run in it (the REPORT's conformance row: llama.cpp, sd.cpp, whisper.cpp and
kokoro after their move to AF_UNIX or stdio). Process creation stays the
facade's one owner (`supervision`, FAC-P3); the kit owns the profile, the
grants, the manifest and the launch check (KIT-S1..KIT-S6). Grants are
applied at install, never per launch: the GPU test spent about 10 seconds
granting a venv's 40,000 files at each launch, and a per-launch grant would
race a second launch's revoke. A launch only reads the access lists, so its
check costs milliseconds. Files a launch needs outside the grants, such as
models the person adopted in place under another owner's folder, reach the
engine as lent read-only handles (KIT-S6): the owner's MuseTree models in
`C:\ProgramData\ASUS\AICreator` are owned by SYSTEM and grant this account
read only, so this account cannot add a grant there without an
administrator.

## 2026-10-02: the kit leaves the model host

The owner decided that the PyTorch engine moves out of the model host into
its own provider (VISION 2026-10-02, "OA may supply an engine process").
A scan of the model host found that the PyTorch kind shared four parts with
its sd-server and llama.cpp kinds: the engine process launch, the video@1
operation table, model file placement, and the holder pieces both use. The
coordinator placed them here rather than in `abstraction-inference`'s
`providerhost`, whose engine adapters review finding D4 already names as a
defect in the inference core.

The rules moved with the code: MODELHOST-E1 (exit records) is KIT-P3, the
model host's engine supervision and socket watch are KIT-P1, P5 and P6, its
places record is KIT-M1 and M2, and its video operation table is KIT-V1 to
V6. The model host keeps its own rules for its engines and names the kit's
parts through aliases, so its tests read unchanged.

With the PyTorch provider the model host's command parts that both
providers run moved here too: the searches, set-folder adoption and `models
protect` (`modelplace`, KIT-M3), `gpu list` and `gpu clear` (`quarantine`,
KIT-Q1), the trusted runtime programs (`runtimeprog`, KIT-E1) and the exit
record's report (`engineproc.Report`, KIT-P3). The model bridge
(`trustedRuntimePrograms` in `modelbridged`) and the Lemonade provider
(`setup.Resolve`) keep their own copies of the runtime-program check; they
are the next consumers to move (TODO D5f).

The review of the branch found `modelplace` importing the model bridge's
`bridge.Source`, a provider-to-provider dependency the import-direction check
refuses. The store search now reads the kit's own `StoreFiles` interface: the
model host adapts the model bridge's source to it, and the PyTorch provider
searches its `--from` folders alone.
