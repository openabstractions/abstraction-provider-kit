# Provider kit contract

The provider kit holds the parts every local-engine provider shares: the
engine process it starts and supervises, the typed refusal of a load, the
model files it proves where they lie, and the operation table of
`abstraction.inference/video@1`. A
provider composes them with the resource kit's `holder` and `gpufault`; the
kit carries no engine of its own and no provider's vocabulary beyond the
words below.

Binds: [inference.thrift](https://github.com/openabstractions/abstraction-inference/blob/main/inference.thrift)

The conventions every OA contract carries are stated once in [CONVENTIONS.md](https://github.com/openabstractions/abstractions/blob/main/idl/CONVENTIONS.md); a rule here that applies one cites its `CONV-` id.

## [KIT-P1] One owned engine process

`engineproc.Start` MUST start an engine only from the environment its caller
built; an empty environment is refused and nothing is inherited. On Windows
the engine MUST be assigned to a job with kill-on-close before its code runs,
with the caller's per-process memory limit, and its exit MUST read as ended
only once every process of the job is confirmed gone. Outside Windows the
engine is a direct child and a memory limit is refused.

## [KIT-P2] The engine's log

An engine's output MUST be kept only in a log of the provider's private
state, bounded per file with one earlier file kept. It carries prompts and
request bodies and MUST NOT reach a caller.

## [KIT-P3] One exit record per end

Every end of an engine MUST produce one exit record naming the engine, the
model it was started for, its process id and what its supervisor saw: the
code or signal, the run time, the memory peaks and whether the job's limit
refused an allocation. The record goes to the provider's exit hook and, when
the provider owns the stream, as the last line of the engine's log.

## [KIT-P4] The device-lost line

A provider that listens for an engine's own device-lost line reads it on the
engine's output and binds it to the process that printed it. The line counts
as a lost device only once that engine exits or fails its own readiness
twice within `DeviceLostConfirm`; it is then reported to the provider's GPU
fault handling (resource CONTRACT.md RES-G1).

## [KIT-P5] The engine as a holder component

`engineproc.Owned` is a `holder.Owned`: its readiness is the engine's own
probe within the provider's budget, then the engine's socket rule (inference
INF-EL2). The socket rule is checked again every watch interval while the
engine runs; a broken rule stops this engine by its own process. A server on
the engine's socket outside its tree refuses readiness with `engine:peer`.
A probe whose every connect for `UnreachableFor` (3 s) failed with an
error a starting engine never gives (enginehttp `ErrEngineUnreachable`, inference
INF-EL2) refuses readiness with `engine:unreachable`, naming the system
error, instead of waiting out the provider's budget.

## [KIT-P6] A stop targets one process

A stop MUST end only the process tree the provider started and report it
stopped only once that tree's exit is confirmed.

## The engine's sandbox

`sandbox` runs an engine OA launches inside a Windows AppContainer with no
network capability (research/engine-sandbox/REPORT.md). The container is the
engine's network containment: it reaches no TCP, DNS or UDP, its children
included, and needs no administrator. Process creation inside it is the
facade's (`supervision.Options.Container`, FAC-P3); this package owns what
the container is given, for every provider whose engine runs contained.

## [KIT-S1] One profile per engine kind

An engine kind's profile is `oa.engine.<provider>.<kind>`, with `.<label>`
after it for a second installation of the same kind; each part is lower-case
letters, digits and hyphens, and the name is at most 64 characters. The
container has no network capability: no `internetClient`,
`internetClientServer` or `privateNetworkClientServer`. Its one capability is
the facade's `ContainerCapabilitySID` (FAC-P3, a TEMPORARY EXCEPTION, KIT-S7),
which reaches only what an access list names it for. LPAC, which also refuses `socket()` itself, is
not used: an engine whose libraries import asyncio, as PyTorch's do, cannot
start there.

## [KIT-S2] Grants are applied once, at install or update, and recorded first

`Begin` takes the sandbox's lock alone, records the phase `installing` in
the manifest before anything else, and creates the profile or adopts the one
a stopped install left. `Setup.Grant` refuses a path that is not absolute,
not local (identity `LocalPath`), absent, or has a link or junction in any
component. The manifest binds the profile to its derived SID and records
each grant's file identity. ACL reads and writes use the checked file handle,
not a fresh path lookup; a changed identity refuses before mutation. A path
with a pre-existing ACE for that SID is refused because removal could not
restore it. A path new to the manifest is recorded with the SDDL of its access list before the
grant, then granted: read and execute (`rx`), read and execute plus
`FILE_WRITE_ATTRIBUTES` (`rxa`), or modify (`rwd`), inherited by what a folder
holds. `rxa` is a TEMPORARY EXCEPTION (KIT-S7): Bun 1.3.14's module loader
opens each module it imports with `FILE_WRITE_ATTRIBUTES`, so a JavaScript
package imported inside the container needs it. A path already recorded is granted again, so files an
update put in a granted folder carry the grant; its snapshot stays the first
install's, and a grant only widens. `Commit` records the phase `ready`. A
launch never grants.

## [KIT-S3] A launch checks the sandbox and holds it

`Prepare` refuses a launch with a reason: `ErrBusy` while Setup holds the
lock or the phase is not `ready`; `ErrNotInstalled` without a manifest, or
when the profile is missing or has another SID; `ErrNotGranted` when a
recorded grant is no longer on its path, when a path the launch needs lies
at or below no grant that covers its access (identity `SameFile` and
`Within`), or when that path's own access list does not carry the grant, as
a file moved into a granted folder after Setup does not. A prepared launch
holds the lock shared until its engine has ended, so Setup cannot change
the sandbox under a running engine (`ErrInUse`), and two engines of one kind
run in it at once. The engine starts with `engineproc.Spec.Container`; a
stop mid-launch ends its contained tree and leaves the sandbox as Setup left
it. Once installed, the check and the contained start add 14 ms to a launch
on the owner's machine (`TestContainedLaunchCost`: a 2.4 ms check and a
12 ms longer start, medians of seven); the target is under a second.

## [KIT-S4] Removal restores every access list and deletes the profile

`Remove` first validates the manifest's paths, identity, profile/SID pair and
saved access lists, then takes the lock alone and revalidates. It records the phase `removing`, revokes every
entry for the container on each recorded path, applied or not, including
nested grants in either order. After all revokes, it compares each
access list with its snapshot (`Removal.Changed` names a list someone else
changed since), confirms no path at or below a recorded one still names the
container, deletes the profile, and deletes the manifest and its lock. It
refuses while an engine runs (`ErrInUse`).
A recorded grant path that has disappeared is unresolved: a rename could
have moved its original ACE outside the recorded tree. Removal retains the
manifest and profile until that path is restored for checked revocation.

## [KIT-S5] A stopped install is found and removed

An install stopped after any step, as a crash stops it, leaves the phase
`installing`, which refuses every launch, and a manifest naming every path it
touched, so `Remove` takes it all back. `Profiles(prefix)` lists the profiles
the system records for this account, a left one included.

## [KIT-S6] Files outside the grants are lent, never granted by a launch

A provider gives a contained engine a file it may not open by path, such as a
model the person adopted in place, by `engineproc.Process.Lend`: a read-only
handle duplicated into the running engine (facade FAC-P4), which reads the
bytes through it and cannot write them. Outside Windows every call of
`sandbox` is refused (`ErrUnsupported`) and `engineproc.Start` refuses a
container.

## [KIT-S7] Ancestors get attributes, never contents

A program that resolves its folder by lstat-ing every ancestor from the drive
root needs `SYNCHRONIZE` and `FILE_READ_ATTRIBUTES` on each ancestor itself,
as a non-inherited entry; a grant on the parent does not give it.
`Setup.GrantAncestorAttributes`, called after every `Grant`, takes each folder
above a granted path that no folder grant covers, once by file identity. A
folder the container already reaches (its own SID, ALL APPLICATION PACKAGES or
the facade's `ContainerCapabilitySID`) is left alone. A folder whose access
list this account controls, as owner or by its own entry with `WRITE_DAC`, is
recorded in the manifest with its snapshot, access `sa`, before it gets one
allowed entry with no inheritance flag, placed after the explicit entries.
Any other folder, such as a drive root or the profiles folder, is reported as
unreached and left alone. Every Attributes read and write goes through one
handle bound to the recorded file identity and `SetKernelObjectSecurity`,
which changes that object alone, keeping the list's protected and
auto-inherited flags; `SetSecurityInfo`, `SetNamedSecurityInfo` and icacls
re-propagate inheritance below the folder and are never used for it.
`Prepare` checks the entry, and `Remove` takes it back the same way, after a
crash too, and compares the folder's own list with its snapshot.

TEMPORARY EXCEPTION (owner, 2026-10-06): the per-machine installer gives
`ContainerCapabilitySID` the same entry on the system drive's root and the
profiles folder (`AttributeRoots`, `GrantRootAttributes`), and its removal
takes exactly that entry back (`RevokeRootAttributes`). Both go, with the
capability, once the application that needs them resolves its folders
natively in a released, pinned version.

Tests: `TestAncestorAttributesWithAFakeWriter`,
`TestReadExecuteAttributesAccess`, `TestReadExecuteAttributesGrantWithAFakeWriter`,
`TestAncestorAttributesStoppedInstall`,
`TestAncestorAttributesRefuseAForeignEntry`, `TestAttributeEntryText`,
`TestAttributeJudgements`, `TestAncestorAttributesOnARealTree` (a stale
sibling stays stale under the non-propagating writer and is rewritten by
`SetSecurityInfo`), `TestRootAttributesOnAFolder`.

Tests: `TestProfileName`, `TestInstallLaunchRemove`,
`TestAnUpdateGrantsWhatItMovedIn`, `TestSetupAndLaunchesExcludeEachOther`,
`TestAStoppedInstallIsRemoved`, `TestAMissingGrantOrProfileRefusesTheLaunch`,
`TestAGrantRefusesALink`, `TestContainedEngineBoundaries` (nothing the
engine or its child sends reaches the test's loopback TCP listener or UDP
receiver; with `OA_PROVIDER_KIT_OUTSIDE_ADDRESS` naming an address off the
machine, TCP to it, DNS and UDP are blocked too; the state folder written, Documents and an
ungranted file denied, an OA pipe that admits the container answers, a lent
handle reads and does not write), `TestContainedLaunchCost`,
`TestAStopMidLaunchLeavesTheSandboxWhole`.

## [KIT-R1] The typed refusal

A refusal carries a word the caller reads and a detail the provider's log
keeps. Neither carries engine text.

## [KIT-R2] The words of a refused load

A load the lease refused reads the resource kit's lease word
(`holder.Failure.Word`); a refusal the load made itself reads its own word;
any other failure reads `unavailable`. A GPU fault refusal keeps the Guard's
word (`gpu:standby`, `gpu:resetting`, `gpu:quarantined`). A video or image
whose engine could not load and named no word fails `engine:unavailable`.

## [KIT-M1] Model files proved where they lie

`modelplace.Place` MUST find each pinned file through the provider's
searches, prove it in place by size, SHA-256 and, when pinned, its Git blob
SHA-1, and record where it lies. It copies nothing (VISION 2026-10-01). A
path through a junction or symbolic link is refused. A store search reads
the provider's `StoreFiles`, the stores' names and a whole stored file's
path; the kit imports no provider, and a provider that reads stores adapts
its own source.

## [KIT-M2] The places record

A places record names each pinned file once, with its absolute path and the
owners it may be trusted under: the machine's owners and this account alone.
A record naming another owner, a file of no pin, or missing one is refused;
a refusal names the command that writes the record.

## [KIT-M3] OA's model folders

`modelplace.AdoptModelFolder` makes a folder directly under OA's models root
a model folder of this account (identity ID-D3) and leaves every other folder
as it is.

## [KIT-M4] No copy, no download

The kit downloads no model and copies none; a provider's Setup stages files,
and the kit proves them where they lie.

## [KIT-V1] Admission and the queue

`videoops.Table.Start` admits a video for its caller (inference INF-G5): a
request_id the caller holds names its operation again; one model; a request
the kind refuses is refused before it is queued; a quarantined fingerprint
is refused at once (resource RES-G3). The table runs one video at a time and
refuses `queue:full` and `operations:full` past its bounds.

## [KIT-V2] The model set's bounds

A request is resolved to the set's preset for size auto and rate zero, its
frames aligned to 4n+1; a value outside video@1 is invalid, and one outside
the set's sizes, frames, rate or first-frame use is unsupported_feature.

## [KIT-V3] The first frame

A first frame is read from the provider's own store by digest and becomes the
provider's own PNG of the exact size; a digest the store does not hold is
`content:unknown`, and nothing is generated from the prompt alone.

## [KIT-V4] Cancel

A queued video leaves the queue; a running one is stopped by its kind, which
reads the operation's cancel.

## [KIT-V5] Retention

An ended operation and its request_id stay held for the retention, at least
24 hours; its result is removed from the store with it unless another held
operation delivers the same digest. The rule covers every output a kit
provider generates, an image as well as a video. A provider that publishes
through `providerhost` `OutputWriter` turns it on with `RetainOutputs`: each
publication is recorded, and `SweepOutputs` removes a copy 24 hours after its
last publication unless a content call named it within a reader's idle
lifetime. PyTorch's image path uses it. Other `OutputWriter` providers keep
their outputs until they turn it on.

## [KIT-V6] The GPU around a video

A video waits while the machine is in Modern Standby or the driver is coming
back from a reset, holds the wake lease while it runs (resource RES-G5), and
reads `gpu:reset` and quarantines its fingerprint when its engine is lost in
a recorded reset; any other loss reads `engine:lost`.

## [KIT-Q1] The GPU quarantine commands

A provider's `gpu list` prints each request its GPU fault handling paused
after a driver reset, with its key, strikes, until when and what it
dispatches, and names a damaged quarantine file with where it was moved;
`gpu clear KEY` (or `--all`) lets a paused request run again (resource
RES-G3). Only a person runs them.

## [KIT-E1] The runtime programs a provider admits

A provider's endpoint admits the runtime programs its installation names: the
installed runtime beside the provider (both forms on Windows), or the clean
absolute paths `--runtime-program` names. The connecting process never
names them.

## Reviewed engine bindings

The clauses below are provider-binding requirements. Their historical `INF-`
rule IDs remain unchanged; the corresponding inference sections forward here.
The current shared implementation is in this module's Go packages. Generic
authenticated-file checks belong to facade `programfiles`; generic child
environments belong to facade `supervision`. The mandatory pure image request
safety is shared from inference `mediationcontent`, with the same typed reasons.
[The 0.8 migration guide](MIGRATING-0.8.md) maps the changed Go import paths.

The inference-owned prerequisites remain there: [INF-A2 and INF-A4 admission](https://github.com/openabstractions/abstraction-inference/blob/main/CONTRACT.md#admission),
[INF-I1 image edits](https://github.com/openabstractions/abstraction-inference/blob/main/CONTRACT.md#image-edits)
and [INF-EL1 child environment](https://github.com/openabstractions/abstraction-facade/blob/main/CONTRACT.md#generic-child-environment).
References to these rules below compose those requirements with the binding;
their ownership does not move with these engine clauses.

## llama.cpp

A provider that starts llama-server enforces the conditions of the llama.cpp
b10825 security review
(`research/security-reviews/llama.cpp-b10825-win-vulkan-x64.md`) that concern
its options, its environment and the media it is sent. The shared provider
kit `go/llamacpp` holds them once; the Lemonade and modelhost
providers use it, and each may refuse more on top.

**[INF-LC1] Forbidden options.** The provider never passes, and refuses in
any configured or requested argument, the options of review condition 2 in
every spelling b10825 accepts, alone or as `--option=value`:
`--sleep-idle-seconds`, `--host`, `--rpc`, `--tools`, `--tools-runtime`,
`--agent`, the MCP options, `-hf` and its `--hf-*` forms, `--model-url`,
`--mmproj-url`, `--docker-repo`, `--spec-draft-hf`, `--models-dir`,
`--models-preset`, `--media-path`, `--path`, `--ui-config-file`,
`--slot-save-path`, `--log-file`, `--log-prompts-dir`, `--video-ffmpeg-dir`
and `--cors-origins`. A provider's own allowlist of options a person may add
admits none of these spellings.

**[INF-LC2] Forbidden environment names.** llama-server never receives a
variable named `GGML_*`, `MTMD_*`, `LLAMA_*`, `HF_*`, `HTTP_PROXY`,
`HTTPS_PROXY`, `ALL_PROXY` or `NO_PROXY`, in any letter case, whatever the
provider's allowlist says. `LLAMA_API_KEY` is the one exception: a provider
that makes a per-launch key passes it under that name, and a provider that
makes none refuses it.

**[INF-LC3] Chat media.** A chat content part reaches llama-server only as
text, as an image the provider decoded and encoded itself, or, where the
provider admits audio, as audio it recognised. An image URL must be a
base64 data URL declaring `image/png` or `image/jpeg` whose bytes are that
format, at most 16 MP by its header before any pixel is decoded; it is
replaced by the provider's own 8-bit NRGBA PNG data URL. Audio data must be
WAV, MP3 or FLAC by its leading bytes, given as OpenAI's raw base64 in
`input_audio.data` or as a base64 data URL declaring `audio/*`; it is
replaced by the provider's own standard base64 of the same bytes, raw in
`data` and as a data URL naming the detected type in `url`. An http(s) URL,
a `file:` URL, video, WebP, GIF, BMP, any other image or part type, and data
that does not decode are refused before the request leaves the provider.

## stable-diffusion.cpp

Image work may reach stable-diffusion.cpp's `sd-server` through any
OpenAI-compatible model server: Lemonade runs it for its image routes, and
`sd-server` serves the OpenAI routes itself. The conditions of the
stable-diffusion.cpp master-843-462d675 review (`../../research/security-reviews/stable-diffusion.cpp-master-843-462d675.md`)
and its video addendum (`../../research/security-reviews/stable-diffusion.cpp-master-843-462d675-video-addendum.md`)
that concern request text and size are held once in the provider kit
`go/sdcpp`. Inference applies them to every image request, and
the Lemonade provider applies them again to each body it forwards.

**[INF-SD1] Engine directive.** `sd-server`'s `/v1/images/*` and
`/sdapi/v1/*` routes read request parameters, LoRA file paths included,
from a JSON block between `<sd_cpp_extra_args>` and its closing tag in the
prompt. An image request whose prompt or any extension value carries the
opening tag `<sd_cpp_extra_args` is `invalid` with reason
`prompt:engine-directive` at step 1 of INF-A2, whichever model server would
serve it. The match ignores letter case and folds full-width forms
(U+FF01..U+FF5E), the small less-than sign and low lines (U+FE64,
U+FE4D..U+FE4F), the long s, dotless i and Kelvin sign, and drops format
characters (category Cf) and non-spacing marks (Mn) before comparing. A
provider forwarding image work toward `sd-server` refuses the same tag in
every text value of the body; that refusal ends the request `invalid` with
the same reason.

**[INF-SD2] Image size.** `sd-server` decodes request images against the
request's width and height and allocates their product in C `int`
arithmetic; with size `auto` it takes the size from the input image. An
OpenAI-compatible model server receives a size of 64..4096 on each side and
at most 4,194,304 pixels (2048 × 2048); a larger size is
`unsupported_feature` with reason `size:<width>x<height>` at step 3. Each
edit input sent to such a server is a PNG, JPEG or WebP whose header, read
before any pixel is decoded, names at most 4096 on each side and 4,194,304
pixels. A larger input is `unsupported_feature` with reason
`size:engine-bound` at step 5, and a header that does not parse as the
declared type is `invalid` with `content:media-type`. A provider's
forwarding refusal of a size or input beyond that bound ends the request
`unsupported_feature` with reason `size:engine-bound`.

**[INF-SD3] Request form.** A provider forwarding image work toward
`sd-server` forwards only the fields inference's OpenAI-compatible adapter
sends (`model`, `prompt`, `n`, `size`, `response_format`, the `seed` its
`Config.ImageFields` writes as an integer 0..2147483647 (INF-A4), and the
edit files `image` and `mask`), each once, as its own encoding of the checked
values. `sd-server` reads a mask's luminance, white marking the area to
change, and ignores alpha; the Lemonade provider converts an INF-I1 mask to
that form.
Any other field or part, a repeated or transfer-encoded part, and a body
that does not parse end the request `invalid` with reason
`request:engine-form`. It reads a body only while it holds its request slot,
bounded by two input images of `Config.MaxImageBytes` and the text fields.
It serves no model that resolves to a Lemonade collection: an Omni
collection's tool calls send `generate_image` and `edit_image` to
`sd-server` past these checks.

A provider that launches `sd-server` itself with a video model applies the
video addendum's launch and request conditions, also held in
`go/sdcpp`:

**[INF-SD4] Command line.** The provider builds `sd-server`'s arguments from
typed fields (`sdcpp.Args`): `--listen-ip 127.0.0.1` always, the port it
chose, `--diffusion-model`, `--t5xxl` and `--tae` naming a Wan set's pinned
files or `--diffusion-model`, `--llm`, `--vae`, `--audio-vae` and
`--hires-upscalers-dir` naming an LTX set's pinned files and its upscaler's
folder (LTX addendum, amendment 1), and only the reviewed device, memory and logging options (`--fa`,
`--diffusion-fa`, `--diffusion-conv-direct`, `--vae-conv-direct`,
`--offload-to-cpu`, `--auto-fit`, `--max-vram` with one number,
`--backend` with `cpu`, `vulkan0`..`vulkan7` or module assignments of them,
`-t`, `--vae-tiling`, `--temporal-tiling`, `--vae-tile-size`, `-v`,
`--color`). `sdcpp.CheckFlag` refuses the addendum's condition 4 options by
name in every spelling, `-M`/`--mode`, `--rpc-servers`, `--serve-html-path`,
the other directory options, every other model path, `--mmap`, every
default-image and prompt-file option, `--hires*`, `--sigmas`, the cache
options and `-l`/`--listen-ip` other than `127.0.0.1` among them, before any
reviewed option is read.

**[INF-SD5] Environment.** The engine starts from the provider kit's clean
environment (INF-EL1), and `sdcpp.ForbiddenEnvironment` refuses names
starting `GGML_`, `SD_`, `VK_` or `HF_` and the proxy variables in any letter
case even if one were added to the provider's allowlist.

**[INF-SD6] Video request.** Every body sent to `/sdcpp/v1/vid_gen` is built
by `sdcpp.BuildVideoRequest` from the provider's preset and checked by
`sdcpp.CheckVideoRequest` before it leaves: one JSON object with only
`prompt`, `negative_prompt`, `width`, `height`, `video_frames`, `fps`,
`seed`, `sample_params` (`sample_steps`, `sample_method`, `scheduler`,
`flow_shift`, `guidance.txt_cfg`, and `custom_sigmas` when the preset sets
its own table), `init_image`, `output_format` and `output_compression`,
each once, and `hires` for a preset's two-stage size. A two-stage size is
sent at half its width and height, a multiple of 32, with `hires` holding
exactly `enabled` true, `upscaler` (a name with no separator or model-file
extension), `steps` 1..8 and `custom_sigmas` (`steps`+1 values in 0..1,
strictly decreasing, ending at 0), and no `init_image`. An LTX set's frames
align to 8n+1. The size is one of 832×480, 480×832, 1280×704, 704×1280 and
512×512, and a provider's model set may narrow it; `video_frames` is 4n+1
in 5..121; `fps` 1..30; `sample_steps` 1..8; `txt_cfg` 0..10;
`flow_shift` 0..20; `custom_sigmas` is `sample_steps`+1 values in 0..1,
strictly decreasing and ending at 0, set by the preset alone; each prompt at
most 2,048 bytes of UTF-8 with no control character or line break (the
engine echoes it into its log); `output_format` `webm` or `avi`. Any other key is
refused by name. A refusal ends the request with reason
`video:engine-bound`, or `size:engine-bound` for a size.

**[INF-SD7] Video answer.** A job answer is read with a cap of 100 MiB. A
completed answer whose format or `mime_type` differs from the request's
(`video/webm`, `video/x-msvideo`), whose `frame_count` or `fps` differs from
the values sent, whose decoded container exceeds 64 MiB, or whose leading
bytes are not EBML `1A 45 DF A3` or `RIFF…AVI ` is refused, and the engine's
error text is never read. A WebM whose sound the engine wrote as 16-bit PCM
(`A_PCM/INT/LIT`) is delivered with that sound encoded as Vorbis and the video
frames unchanged (`sdcpp.VorbisSound`); a clip it cannot re-encode is delivered
as the engine wrote it, and the provider's log says why.

**[INF-SD8] First frame.** A caller's first frame reaches `sd-server` only as
the provider's own PNG: `sdcpp.FirstFrame` admits PNG or JPEG whose bytes
are the declared type, at most 16 MiB and 4096 on each side by its header,
crops its centre to the request's aspect, scales it to exactly the request's
width and height, flattens transparency onto black and encodes it again. Any
other frame is refused with reason `frame:engine-bound`.

## Video encoding

**[INF-VE1] Raw frames become H.264 in MP4 with the platform's encoder.** A
provider whose engine returns raw frames encodes them with the kit's
`videoenc`, never with an encoder it bundles. Frames are RGB24
or RGBA rows, top to bottom, of one size with even sides from 32 to 4096,
at 1 to 240 fps; anything else is refused before an encoder starts
(`ErrOptions`). On Windows the kit drives Media Foundation's Sink Writer,
which ships with Windows, through OA's own COM binding: an MP4 container
(`MF_TRANSCODE_CONTAINERTYPE` MPEG-4) holding one H.264 High-profile
progressive video track at the frames' rate and size, at the caller's
average bit rate (at most 2^32 - 1 bit/s, the attribute's width) or 0.4
bits per pixel per frame and at least 1 Mbit/s, each frame lasting exactly
1/fps in 100 ns units. Writes are safe from several goroutines at once;
each frame takes the next index on the encoder's own thread, in arrival
order. The hardware H.264 encoder runs when the machine has one and the
caller did not ask for software; Microsoft's software encoder runs
otherwise, and the answer names which ran (an encoder transform that names
a hardware URL is hardware). The MP4 is fast start: `moov` comes before
`mdat`, moved there by the kit with every chunk offset (`stco`, `co64`)
moved with the data, so a browser plays it while it downloads. When a
32-bit offset would pass 4 GiB once moved, every `stco` becomes a `co64` of
the same offsets; `moov` grows by 4 bytes per chunk, and every offset grows
by that final size. A fast start that fails after the encoder finished
never discards the video: the encoder's own file, `moov` last, is placed,
and the answer reads `FastStart` false. The writer
writes a temporary name in the destination's folder, and Close moves the
finished file to the destination without replacing anything there
(`noreplace`, the rename `modelpack` uses); a destination
that already exists is refused at Create, and one that appears before
Close is refused then and left as it is. A video of no frame, a writer
that cannot finish and an aborted encoder leave no file of theirs. Every
other platform answers `ErrNoSystemEncoder`; no fallback encoder exists.
The delivery's media type is `video/mp4`.

## whisper.cpp

A provider that forwards transcription toward `whisper-server` enforces the
conditions of the whisper.cpp v1.8.4 security review
(`research/security-reviews/whisper.cpp-v1.8.4-win.md`) that concern its
options, its environment, the request and the answer. The shared provider
kit `go/whispercpp` holds them once, with the WAV reader and
writer of `go/wavfile`; the Lemonade provider uses them, and a
provider may refuse more on top.

**[INF-WC1] Options.** `whisper-server`'s command line is exactly
`-m <pinned model> --port <port>`. The provider never passes, and refuses
alone or as `--option=value`, `--host`, `--public`, `--request-path`,
`--inference-path`, `--convert`, `--tmp-dir`, `-vm`, `--vad-model`, `-oved`
and `--ov-e-device` (review condition 3).

**[INF-WC2] Environment.** `whisper-server` never receives a variable named
`WHISPER_*`, `OVERRIDE_VITISAI_*`, `KMP_*`, `GGML_*`, `HF_*`, `HTTP_PROXY`,
`HTTPS_PROXY`, `ALL_PROXY` or `NO_PROXY`, in any letter case (condition 4).

**[INF-WC3] The request.** The provider builds the multipart body itself.
`model` is `Whisper-Large-v3-Turbo`. `file`, named `audio.wav` of type
`audio/wav`, is the provider's own WAV of the caller's samples: the input
must be RIFF/WAVE with format tag 1, 16-bit, one or two channels at
8000..48000 Hz, a RIFF size equal to its length minus 8, one fmt chunk and
one data chunk of whole sample frames, at most 16 MiB; the provider writes
its own 44-byte header and the samples, so the bytes begin `RIFF` and a size
below 2^24. Every other input format is `unsupported_feature` with reason
`format:<media type>`, and malformed WAV ends the request `invalid` with
reason `audio:wav`. `language` is `auto` or
the primary subtag of the request's language in whisper's table, `prompt`
at most 1024 bytes of UTF-8 without NUL, `temperature` a finite number in
0..1, `response_format` one of `json`, `verbose_json`, `text`. No other
field reaches the engine (condition 7).

**[INF-WC4] The answer.** The provider reads at most 4 MiB with status 200,
of type `application/json` for `json` and `verbose_json` and `text/plain`
for `text`, and refuses anything else `unavailable` (condition 8).

## Kokoros

A provider that forwards speech toward Kokoros' `koko` enforces the
conditions of the Kokoros b17 security review
(`research/security-reviews/kokoros-b17-win.md`) that concern its command
line, its environment, the request and the answer. The shared provider kit
`go/kokoro` holds them once; the Lemonade provider uses it.

**[INF-KO1] Command line.** `koko`'s command line is exactly
`-m <pinned model> -d <pinned voices> openai --ip 127.0.0.1 --port <port>`,
optionally with `--instances 1` or `2`; the `text`, `file` and `stream`
modes and any other address are refused (review condition 3).

**[INF-KO2] Environment.** `koko` never receives a variable named
`PIPER_*`, `ESPEAK*`, `ORT_*`, `SSL_CERT_*`, `HTTP_PROXY`, `HTTPS_PROXY`,
`ALL_PROXY` or `NO_PROXY`, in any letter case (condition 4).

**[INF-KO3] The request.** The provider builds the JSON body itself and
sends only `model` `kokoro-v1`, `input`, `voice`, `response_format` `pcm`,
`speed` and `stream` false. `input` holds 1..4096 characters of NFC text:
carriage returns become line feeds, tabs spaces, format characters and
variation selectors are dropped, and NUL, every other control character
except line feed, combining marks and characters NFC would change are
`unsupported_feature` with reason `text`. `voice` is one American or
British voice of the pinned voices file (`af_*`, `am_*`, `bf_*`, `bm_*`),
never a mix; another
voice is `unsupported_feature` with reason `voice:<name>`. `speed` is a
finite number in 0.5..2.0. The provider writes the WAV header of a `wav`
delivery itself; every format but `wav` and `pcm` is `unsupported_feature`
with reason `format:<word>`. It never sends `initial_silence`, `lang_code`,
`stream_format`, normalization, download or volume fields, or load options
(condition 6).

**[INF-KO4] The answer and a crash.** The provider reads at most 64 MiB
less the 44-byte header, with status 200, of type `audio/pcm` or `audio/l16`
at 24000 Hz, little-endian, one channel, and an even, non-zero byte count
(condition 7). A dropped connection or engine exit ends the request
`unavailable` with reason `engine:crashed`; the provider never resubmits
the text, names it in its log by SHA-256 only and admits the next speech
request only after a backoff (condition 8). The backoff refuses speech
alone. A provider whose engine process also serves other profiles stops it
only when no other model has admitted or running work, and its idle stop
(condition 9) likewise waits for work of every profile to end.

## Engine launch


A provider that starts a third-party engine starts it with a clean
environment and confirms where it listens. The shared provider kit
`go/enginelaunch` implements both, and
`go/enginehttp` is the client that reaches the engine; the
Lemonade, TheNoise and modelhost providers use them, each with its own
variable names and values.

**[INF-EL2] No network socket, and the socket's server is the engine.** An
engine that can serve HTTP on an AF_UNIX socket path is given one in a
launch directory the provider creates for that launch alone
(`enginelaunch.NewSocketDir`) under a fixed per-user socket root,
whatever the provider's state folder (`SocketRoot`):
`%LOCALAPPDATA%\oa\s` on Windows, the local application data folder read
from the token's known folder; `$XDG_RUNTIME_DIR/oa` on Linux, or
`/tmp/oa-<uid>` when the runtime directory is unset or is no 0700
directory of this account; `$TMPDIR/oa` on macOS with the same fallback.
On Windows, the kit asks the account profile-view owner whether this
process sees a packaged app's redirected profile. It refuses the default
socket root with `ErrSocketRoot` when the view is virtualized or cannot be
read, before making a launch folder there (VISION 2026-09-17).
For every Windows launch directory, including one under a provider's
explicit root, the kit binds and dials a temporary AF_UNIX socket at the
exact launch path before starting the engine. A connect failure refuses
the launch with `ErrSocketTransport` and removes the directory. This
detects a redirected profile path that accepts a bind but cannot connect.
The kit creates an absent root with the private descriptor below and
refuses a root that is a file or a link, that another principal owns, or
that is open beyond this account and SYSTEM (a DACL that is not protected
or that allows any other principal on Windows; any group or other bit, or
another mode than 0700, elsewhere) (`ErrSocketRoot`, wrapping the helper's
`privatedir.ErrNotPrivate`). The root and each
launch directory are made and checked by OA's one private-folder helper,
abstraction-identity's `go/privatedir` (identity ID-D1), which the span
record folder uses as well (INF-MS7). The launch directory
has a random name, is refused if the path exists, carries a protected
DACL of two entries (this account and SYSTEM) on Windows and mode 0700
elsewhere, and holds the socket `e.sock` and a `held` file its provider
keeps locked (no sharing on Windows, an exclusive `flock` elsewhere) from
creation until the directory is removed. The socket path is printable
ASCII of at most 107 bytes (`MaxSocketPath`); a provider refuses a root
that cannot fit one when it starts (`SocketPathFits`). The engine is given
no port. The provider's client (`enginehttp.NewUnixTransport`) dials the
socket and binds the process the kernel names for its server end
(abstraction-identity ID-T8) before the request's first byte, admits it
only as a member of the engine's contained tree by process id and creation
time, or, without a supervised tree, as the process the provider started,
and rechecks the binding on every read and write; a refused server
receives no byte. A connect error a starting engine gives (the connection
refused, the socket file not made yet, a timeout) is returned as it is;
any other system error, such as the WSAEINVAL a Windows AF_UNIX path in a
packaged app's redirected folder reads, wraps `ErrEngineUnreachable`. Every
engine readiness loop ends once its probe's connects have failed with
`ErrEngineUnreachable` without a break for `UnreachableFor` (3 s), naming the
system error (`UnreachableRun`); the provider kit's `engineproc.Wait`
and the Lemonade and TheNoise providers' own loops keep this rule. Once the engine reports ready over that client, and
again at least every 30 seconds while it runs, the provider reads the
socket owner tables and confirms the engine's contained tree owns no TCP
socket in any state (listening, connecting, established or closing,
outbound connections included) and no bound UDP socket, on any address,
loopback included (`CheckSocketOnly` over identity's `AllSockets`). An
engine that fails is stopped by its own process; a check that fails after
the provider loaded a newer engine for the same model stops only the
engine it checked. The launch directory is removed once the
engine's exit is confirmed, after a stop or a crash. A provider removes
the launch directories under its root that no provider holds and whose
socket answers no dial (`SweepSocketDirs`); a directory whose engine is
still starting is held and stays, so providers can share the root.

An engine that cannot yet serve a socket path keeps the loopback rule: the
provider reads the listener and UDP owner tables once the engine reports
ready, and again while it runs, and confirms the engine's contained tree
holds its TCP listener on 127.0.0.1 at the port the provider chose and no
socket beyond loopback (abstraction-identity ID-T7, `CheckLoopback`), and
every request the provider sends the engine, readiness and work alike,
goes through a client that binds each connection to the engine's tree by
the TCP owner table (`enginehttp.NewClient` or `NewClientWithVerifier`,
ID-T1), so a process that takes the port after the engine exits receives
no byte. These
engines are `sd-server` (stable-diffusion.cpp 462d675, launched by the
Lemonade provider and by the model host's video kind), `whisper-server`
(whisper.cpp v1.8.4), Kokoros `koko` (b17) and `lemond` with the engines
it starts (Lemonade 2026.39.1), and TheNoise (0.9.1);
`research/model-bytes/ENGINE-TRANSPORT.md` records what each needs. A
provider applies both rules through one call, `CheckEngineSockets`, which
takes the socket rule for an engine with no port and the loopback rule
for an engine on a port. An engine that fails either rule, or a platform
without owner tables, fails readiness and the provider stops the tree.

**[INF-EL3] The GPU library path gate.** Before every launch that loads
ROCm's hipBLASLt or rocBLAS kernel files on Windows, the provider runs the
kit's one gate (`enginelaunch.GPUPathGate`) on each library's gfx kernel
folder. With Windows long paths off, every path the library opens, the
folder, a separator, the file and the separators the library adds itself
(one more for hipBLASLt, two for rocBLAS), must fit 259 UTF-16 code units
(`MaxLibraryPath`, research/adoption/thenoise-gpu-crash/FINDINGS.md). A
folder too deep runs only through its 8.3 name in the library's
`*_TENSILE_LIBPATH` variable, and only when that name is shorter, resolves
under the engine root's own final path to the same folder, passes no
reparse point from the root down, and fits; otherwise the launch is
refused (`ErrKernelPathTooLong`). TheNoise's `--device cuda` launch and the
model host's PyTorch engine apply it.


## Model memory


A provider host places a model's bytes in memory once and lends them,
read-only, to each engine it launches (VISION.md, 2026-10-01;
`research/model-locators/MEMORY-LENDING.md`). The shared provider kit
`go/modelfiles` implements these rules once; it composes with facade `programfiles` for authenticated files and the kit's
engine process owner. The section is provider-side data: it never reaches an
application or a contract field, and it never crosses machines.

**[INF-MS1] Fill without a file.** `Fill` creates an unnamed,
pagefile-backed section the provider host owns and places the object's
bytes in it from one source: a whole file, a STORED region of a container,
or one DEFLATE stream, decoded with Go's `compress/flate` on one core. A
stored source is read with positioned reads on at most sixteen handles,
unbuffered when its offset is a multiple of 4,096. Each reader maps a
writable view of one 16 MiB chunk, touches its pages, reads into it and
unmaps it, so page zeroing, reads and view teardown run on every reader
(6 GB/s from the Strix Halo's NVMe, 2026-11-12); a DEFLATE or decoded fill
touches its one view's pages on all cores first. Pages are small pages. The
caller counts the section's memory before the fill starts. No source is extracted,
and the fill creates no file, temporary or otherwise. The whole size is
charged to commit once, at creation.

**[INF-MS2] Proof.** `Fill` takes the object's digest, `sha256:` and 64
lower-case hex digits, the digest of its uncompressed bytes that storage
reports. It hashes every byte it places: the whole object, and each
16 MiB chunk for later checks. A DEFLATE stream must give exactly the
object's length and match the member's CRC-32. Bytes whose digest differs
are `ErrDigest`; a source that cannot give them is `ErrSource`; a cancelled
fill returns the context's error. Each of these frees the section before
`Fill` returns. A digest that does not decode as 32 bytes of lower-case hex
is refused before a section exists.

**[INF-MS3] Read-only.** After the proof the provider host unmaps its
writable view and replaces its own creating handle with a duplicate holding
`SECTION_MAP_READ` and `SECTION_QUERY` only, closing the source. From then
on no writable view of the section exists and no handle in any process can
map one. The section's descriptor is the protected DACL `D:P(A;;0x5;;;OW)`:
the owner may query and map for reading, and the OWNER RIGHTS entry takes
away the owner's implicit `READ_CONTROL` and `WRITE_DAC`. Neither the
provider host nor an engine of the same account holding its loan can map
the section for writing, raise a view to read-write, or duplicate a handle
to map-write or `WRITE_DAC`. The proof made at the fill holds for the
section's lifetime. Every loan is read-only (VISION 2026-10-01); no
writable loan exists. The account stays the boundary, as for files: an
engine can copy its read-only handle into another process of the same
account, and a process holding `PROCESS_DUP_HANDLE` on the provider host
can duplicate the host's read-only handle.

**[INF-MS4] Loan.** `Lend` gives one engine an inheritable duplicate of the
section with `SECTION_MAP_READ` and `SECTION_QUERY` only. The engine
receives it at launch through supervision's handle list (abstraction-facade
CONTRACT.md FAC-P1), with the handle value and length on its command line,
and the provider host closes its copy of the duplicate once the engine has
started. The loan is a lease that ends when the engine's process tree has
exited, as supervision confirms it, or when the engine reports that it
released the section. A loan holds the section until it ends: a caller
whose engine does not start ends the loan at once with `End`.

**[INF-MS5] Free.** Sections have no warm-residency rule: a section is
freed when its last loan ends, or when its owner closes it with no loan
open, and lends nothing afterwards. The end that frees the section closes
it to new loans in the same step, so a `Lend` racing that end is refused
with `ErrReleased`. The caller passes `Fill` the release of
the `abstraction.resource` holding it took for the section's memory, and the
kit calls it exactly once: when the section is freed, or when `Fill`
refuses.

**[INF-MS6] Platforms and engines.** The kit is built on Windows. On Linux
the design is a sealed `memfd` (`F_SEAL_SHRINK`, `F_SEAL_GROW`,
`F_SEAL_WRITE`, `F_SEAL_SEAL`) that an engine maps, or opens as
`/proc/self/fd/N`; it is not built, and `Fill` there answers
`ErrUnsupported`. The PyTorch provider's engine receives its model files
as sections through the model bytes receiver (KIT-MB1..KIT-MB6,
abstraction-provider-pytorch PYTORCH-T12). The engines the model host
launches read files at their pinned versions, and the model host declares
`file` only (abstraction-provider-modelhost CONTRACT.md MODELHOST-L1).

**[INF-MS9] Fill from a storage reference.** Storage owns what a fill, its
proof, its seal and its mapping mean (storage CONTRACT.md, Content transfer,
STO-D1..STO-D6); this kit is its one implementation. A fill's source is the
storage type that says where data is: a storage `Ref` (`storage.thrift`:
store, digest, size, opaque locator). `FillRef` takes the reference and the store
that issued it. The store must find the reference's digest now: a
reference it does not find names requested bytes (a reservation `Place`
made, a download request, an inventory want) and is refused with
`ErrRequested`. The store's local binding (`storage.Local`) projects the
reference onto the file the fill reads, at the disk's rate as INF-MS1
reads; a store with no local binding is refused with `ErrSource`, since
bindings never cross machines and a remote runtime fills its own. A
provider whose resolve seam projects a reference to a region or a
compressed member passes that extent to `Fill`. The reference's digest is
a naming key: the fill proves the bytes against it as INF-MS2 does, and
through `SpanRecords` a later fill of the same object is proved by its span
record (INF-MS7) with no second whole-object hash. The download archive is
one such store (`archive.Store`, abstraction-download DL-R44): its
reference names the content-name entry or a listed file found by the hex
in its name or its recorded SHA-256, and a reader that refuses its bytes
reports it through `Refused`, which applies DL-R44's rule for differing
bytes. The PyTorch provider fills from it a model file missing or refused
where it was placed (abstraction-provider-pytorch PYTORCH-T12): local first,
then the archive, and no other ranking; a wider source selection waits for
its own design. A fill from requested bytes, reading a download's verified
stream as it arrives, is not built.

**[INF-MS7] Span record.** OA's own record of an object is its `sha256:`
digest, its length and the SHA-256 of each `ChunkSize` span of its bytes,
made only when OA hashed every byte: by a fill that hashed the whole object
(INF-MS2, INF-MS8) and by the packer, which hashes the object it packs
(INF-MP6). The kit's `SpanRecords` keeps one record per digest in a folder
of the provider host's private state, bound to the span size, and is the
one place OA reads span digests from before it trusts them (INF-MP1). A
record that does not parse, names another digest or span size, or holds the
wrong number of spans is no record: the next fill hashes the whole object.
A record that could not be written leaves no record, and the fill that
made it reports the failure on its section (`Section.RecordErr`, INF-MS8).
A record is written to a `.record-` file in the folder and renamed into
place; opening the folder removes such files a killed writer left. The
folder is private to this account through OA's one private-folder helper,
abstraction-identity's `go/privatedir` (identity ID-D1), the same one the
engine socket root uses (INF-EL2): created with a protected DACL that allows this
account and SYSTEM, set at CreateDirectory, on Windows and mode 0700
elsewhere; a folder that is a file or a link, that another principal owns,
or whose DACL is inherited or admits anyone else (any group or other bit,
elsewhere) is refused (`privatedir.ErrNotPrivate`). The account stays the
boundary, as for the section itself (INF-MS3). The record lives with the fill and
the span size, beside the engine-file records the same host keeps (INF-F2),
and never crosses machines; `abstraction.storage` keeps the object's
identity (MEMORY-LENDING.md §2, §3).

**[INF-MS8] Fill through a decoder.** `FillDecoded` creates a section as
`Fill` does and lets a decoder place the object's bytes in its writable view
from a STORED region of a file, with no file written. It needs
`SpanRecords`. The fill owns the proof: after the decoder returns, it
hashes every span itself. When `SpanRecords` holds the object's record, the
spans must equal the record's, and the decoder is given them and may refuse
early. Otherwise the fill hashes the whole object against its digest as
well and records the spans (INF-MS7). A decoder may leave bytes for the
hash pass to complete: it returns a function the fill calls once per span,
on several goroutines at once, and the function that call returns gets the
span's bytes in order in blocks of 256 KiB, each just before the fill
hashes it, and completes them in place without writing another span's
bytes. The hash then reads each block once while it is in cache. After the proof the writable view is dropped and the section
sealed (INF-MS3). A record of another length, bytes of another digest, a
decoder's error and a cancelled fill free the section, as INF-MS2 does. A
proven first fill whose record cannot be written returns its section with
no error: the section's `RecordErr` wraps `ErrRecord`, the provider host
logs one line naming the object, and the next fill hashes the whole object
again (INF-MS7).
A decoder reads the region through `RegionReader`, one per goroutine: the
unbuffered reader a stored fill uses (INF-MS1), so the file cache keeps no
copy of the bytes.


## Model bytes


An engine OA launches receives a model's bytes as a model bytes URI in the
argument where it takes a model path today (VISION.md, 2026-10-01;
`research/model-bytes/DECISION.md`). The receivers are the kit's
`go/modelbytes/oa_model_bytes.h`, a single-file C header, and
`go/modelbytes/oa_model_bytes.py`, a standard-library Python module; the
lender's side is `go/modelbytes` and `go/modelfiles`.

**[KIT-MB1] The URI.** `fd:<N>?offset=<bytes>&length=<bytes>[&sha256=<64 hex>][&lease=<M>][&kind=memory]`.
`N` is a descriptor the engine holds: a `HANDLE` value on Windows, a file
descriptor elsewhere. Numbers are decimal with no sign and no leading
zero. `offset` and `length` are required, `length` is above zero and
`offset + length` fits 64 bits. `sha256` is the digest OA proved, in lower
case. `lease` names the engine's end of the lease pipe (KIT-MB4).
`kind=memory` marks bytes OA filled into a sealed memory section. A
repeated or unknown parameter is refused. A string that does not start
with `fd:` is a path, which the receiver maps read-only.

**[KIT-MB2] The descriptor.** On Windows a `kind=memory` descriptor is a
pagefile-backed section with `SECTION_MAP_READ` and `SECTION_QUERY` only,
under the protected descriptor INF-MS3 sets. Any other descriptor is a
file-backed section or a file handle without write, append, `DELETE`,
`WRITE_DAC` or `WRITE_OWNER` access. On Linux it is a regular file or a
`memfd` opened read-only, and `kind=memory` names a `memfd` (its
`/proc/self/fd` link reads `/memfd:`) carrying the `F_SEAL_WRITE`,
`F_SEAL_GROW` and `F_SEAL_SHRINK` seals. The descriptor reaches the engine
by one of two routes: inherited at launch through the handle list
(supervision `Options.Inherit`, `os/exec`'s `AdditionalInheritedHandles`)
at the value the URI names, or duplicated into a live member of the
engine's owned job with exactly that access once the job membership and
container identity are proved (facade FAC-P4; `engineproc.Process.LendHandle`,
`modelfiles.Section.LendTo`), whose value the URI then names.

**[KIT-MB3] The receiver's checks.** `open` parses the URI, then: marks
the descriptor and the lease non-inheritable, so the engine's own children
never receive them; reads the object's type and refuses one that is no
section or file (`handle`); refuses a descriptor whose granted access
allows writing, and a `kind=memory` section to which this process can
raise a handle to `SECTION_MAP_WRITE` or `WRITE_DAC` (`writable`); refuses
a section whose backing differs from the URI's kind (`handle`); refuses
`offset + length` beyond the object's size (`size`); and maps the range
read-only from the offset rounded down to the allocation granularity. It
returns the object's first byte, its length and the digest. `read_at`
copies `n` bytes at `offset` only when `offset + n <= length` (`range`).
Every refusal closes the lease. `release` unmaps, closes the descriptor
and ends the lease, and the Python receiver refuses to unmap while a
tensor or slice still uses the view.

**[KIT-MB4] The lease.** OA creates one pipe per loan and keeps its read
end; the engine holds the write end the URI names. The receiver's
`release`, a refused `open` and the engine's exit each close it, and OA
reads end-of-file: the loan ends (`Loan.EndWhen`) and the section is freed
when its last loan ends (INF-MS5). An engine that copies the bytes to the
device releases at the end of its load; one that reads the mapping in
place keeps the lease until it frees the model. A provider lends a model's
files in the order its engine loads them, one file when the engine asks
for it, and fills the next only once the sections lent before it are
freed (or after 10 seconds, with one log line), so the sections held at
once are one component's.

**[KIT-MB5] Receivers OA ships.** Engines in OA's builds vendor
`oa_model_bytes.h` (`#define OA_MODEL_BYTES_IMPLEMENTATION` in one
translation unit). A Python engine OA launches receives
`oa_model_bytes.py` as an engine file proved and held like its script,
named in the load message with its SHA-256, and imports it only when the
digest matches. A provider gives each sandboxed engine the bytes through
the receiver and no path.

**[KIT-MB6] Conformance.** A receiver conforms when it passes the kit's
suite (`go/modelbytes`): `TestConformance` on Windows and
`TestConformancePOSIX` on Linux (run under WSL 2026-11-12), each against
every receiver as a child started with the case's descriptors inherited.
On Linux the lent object is a sealed `memfd`, a write is an `mprotect`, a
reopen of `/proc/self/fd/N` for writing and a shared writable mapping, and
the wrong-type descriptor is a pipe. macOS is not run yet. On Windows: the bytes
and probes of a filled section through the mapping and through `read_at`;
an offset off every page boundary; `read_at` past the end and at offset
2^64-1 refused; a write through the mapping, a duplicate raised to
`SECTION_MAP_WRITE` and a writable view of the section each refused; the
lease ending at `release` and at exit without release; refusal of twelve
malformed URIs, a descriptor the process never received, a length past
the section, an event handle, an unsealed section, a writable section
handle, a file handle named `kind=memory`, a writable file handle and a
missing path; and the same bytes through a read-only file handle and a
plain path. A receiver an upstream engine carries passes the same suite.


## Model packs


OA's model packs keep 16-bit float weights in an in-house exponent codec
where it loads faster than the disk, and every other tensor as it is
(VISION.md, 2026-10-01; `research/model-locators/GPU-AND-MATRIX-DECOMPRESSION.md`).
The provider kit `go/modelpack` implements these rules once. It
sits beside `modelfiles` because the provider host decodes a pack into the
section it lends to the engines it launches (INF-MS1). It moves to
`abstraction-storage` if packs become a storage object kind. The pack index
and the codec are provider-side data: they never reach an application or a
contract field.

**[INF-MP1] Pack index.** An object's packed bytes are a run of encoding
units. Its index records the object's `digest`, `sha256:` and 64 lower-case
hex digits, which binds the index to the object; its decoded `length`; for
each unit in
order, `offset` and `length` of its decoded bytes in the object,
`packed_offset` and `packed` of its stored bytes in the object's packed
bytes, and `encoding`, which is `stored`, `exp-huffman-v1`,
`exp-fixed3-v1` or `exp-ans-v1`, and for a delta unit `transform` and
`base_offset`; `base`, the base object of an index holding delta units
(INF-MP12); and `spans`:
the SHA-256, as 64 lower-case hex digits, of each `modelfiles.ChunkSize`
(16 MiB) span of the decoded bytes. These are the digests a section records
when the same object fills it (INF-MS2); the span size has one owner,
`modelfiles`. A `stored` unit holds its decoded bytes as they are. Units
carry no digest of their own: the decoder refuses a unit whose structure is
broken (INF-MP3), and the span check catches every other change, located to
16 MiB.

Span digests prove bytes only when they come from OA's own record of the
object (INF-MS7): the spans OA recorded when it hashed the object (INF-MS2,
and `Pack`, which hashes the object it packs), or the spans of an index bound
to the object's whole SHA-256 that OA has verified once. The first fill
from a pack whose spans OA has not recorded hashes the whole object against
its digest and then records the spans; later fills trust the recorded
spans. An index read from a pack alone is never proof.

A reader refuses, before it reads a unit, an index whose units do not cover
the object in order without gaps, that names another encoding, whose
`stored` unit has two sizes, whose `exp-huffman-v1` unit decodes to more
than 2 MiB or stores more than its decoded length plus 2,320 bytes (the
16-byte header and two planes' tables of 1,152 bytes), whose
`exp-fixed3-v1` unit decodes to more than 2 MiB or an odd length or stores
more than its headers, chunks and one escape per eight values (INF-MP8),
whose `exp-ans-v1` unit decodes to more than 2 MiB or an odd length or
stores more than its decoded length plus its 16-byte header (INF-MP11),
or span digests that are malformed or not one per span (`ErrCorrupt`).

**[INF-MP2] Choice per unit.** The packer encodes only the bytes of tensors
whose type is `BF16` or `F16`, as safetensors and GGUF name them, in the
encodings whose measured aggregate decode rate for that type exceeds the
measured read rate of the volume holding the pack: `exp-huffman-v1` for
`BF16` and `F16`, each judged by its own rate, and `exp-fixed3-v1` for
`BF16`. With no measured disk rate nothing is encoded. Such a tensor is cut
into units of 512 KiB from its start. Each unit is tried in every such
encoding, and an encoding counts only where the encoded unit is at most 97
percent of its decoded length. The unit takes the encoding with the
shortest expected load, the larger of its stored bytes over the disk rate
and its decoded bytes over the encoding's decode rate, against its decoded
bytes over the disk rate for `stored`; on a tie the smaller encoding. The
expected load is a heuristic from aggregate rates: it orders the encodings
of one unit and predicts no fill's time. The fixed code wins where `exp-huffman-v1` would wait on its decode (few
cores), and `exp-huffman-v1` where its decode keeps up with the disk.
Quantised tensors, `F32`, every other type and the bytes between tensors
are `stored`, in units of at most 16 MiB. The codec refuses any other type, an odd byte count, an empty unit
and a unit over 2 MiB (`ErrDType`). The packer decodes every encoded unit
before writing it and refuses the pack when the unit does not decode to its
bytes (`ErrCorrupt`).

A machine makes its packs when it stages a model, with its own measured
rates. A pack moved to another machine stays valid there and decodes to the
same bytes; that machine may repack it with its local rates.

**[INF-MP3] The `exp-huffman-v1` unit.** Each little-endian 16-bit value
is rotated left by one bit. Its high byte is then a BF16 value's exponent,
or an F16 value's exponent and three highest mantissa bits; its low byte is
the remaining mantissa bits and the sign. The unit is a 16-byte header and
two byte planes: high bytes, then low bytes. The header holds the format
version 1, the type (1 `BF16`, 2 `F16`), each plane's mode (0 raw, 1
Huffman, 2 constant), the value count and each plane's length, the four
numbers little-endian 32-bit. A raw plane is one byte per value; a constant
plane is one byte. A Huffman plane is 128 bytes of code lengths, two 4-bit
lengths per byte for symbols 0 to 255, then 256 little-endian 32-bit
substream end offsets, then the substreams. The code is canonical
(RFC 1951 §3.2.2), complete, of at least two symbols and at most 12 bits
per code. Substream `t` holds the symbols of value quads `t`, `t+256`,
`t+512` and so on, four symbols per quad and fewer in the unit's last
quad. Each substream starts on a byte and holds its codes least
significant bit first; its recorded end offset follows the byte that holds
its last bit. A decoder can start at any substream. A plane is coded only
where its code saves at least 3 percent of the plane. A unit whose bytes
contradict this layout is refused (`ErrCorrupt`).

**[INF-MP4] Decoding into memory.** `Unpack` reads each unit with one
positioned read and decodes it straight into its place in the caller's
memory, the writable view of the section a provider host fills; it creates
no file, temporary or otherwise. The caller passes the span digests apart
from the index, taken from OA's record of the object (INF-MP1). Once every
unit covering a span is decoded, `Unpack` checks that span's SHA-256
against them. A first fill from a pack whose spans OA has not recorded
passes the index's spans and counts the bytes as proven only after the
whole object's SHA-256 matches its digest. A truncated pack, a malformed
unit, or span digests of the wrong count or form are `ErrCorrupt`, a span
of other bytes is `modelfiles.ErrDigest`, and a cancelled context returns
its error; after any refusal the caller discards the memory. Units decode
on several goroutines, and the bytes equal those of one goroutine.

**[INF-MP5] Built and not built.** The decoders run on the CPU in Go's
standard library, and an NPU decodes `exp-fixed3-v1` units inside a fill
(INF-MP10). A GPU decoder of the same bytes follows once the owner
schedules each GPU run. `Pack` and `WriteZip` take the rates as inputs;
`MeasureRates` measures them (INF-MP9). No model host or bridge binds an
archive yet: STORED file entries are regions a container inventory already
lists, and a binding of the packed kind follows with the first engine that
reads a section. `exp-ans-v1` units (INF-MP11) decode wherever a pack is
read; `Pack` writes them as delta units (INF-MP12) and not for plain
weights, where `exp-huffman-v1` stays the default (`ANS.md`). The NAS
shared store, which will hold bases for their deltas, is not built
(`research/model-locators/MODEL-ARCHIVE.md`).

**[INF-MP6] The OA model archive.** A pack and a model archive are one
format: one zip layer (DECISION.md §7 rule 9;
`research/model-locators/MODEL-ARCHIVE.md` §4) of STORED entries. The first
entry is `oa-archive.json`, the index: format `oa-model-archive`, version 1,
and for each file its slash-separated relative `path`, its zip `entry`, its
weight `format` (`safetensors` or `gguf`) when it is one, its pack `index`
(INF-MP1: object digest, length, units, span digests), and for a weight file
its tensor table: name, dtype, shape, decoded offset and length, the tensor's
SHA-256, and for BF16 and F16 tensors the dtype-normalised digest. That
digest is the SHA-256 of the tensor's values widened exactly to
little-endian F32, a NaN's payload and sign bits carried over unquieted, and
it compares values bit for bit: `-0` and `+0` differ. It covers the values
alone; a deduplication by it also matches the shape. It is compared only
with other dtype-normalised digests, never with a tensor's SHA-256. Then
one entry per file, in index order, its data on a 64 KiB boundary padded
through a zipalign extra field (ID `0xD935`) in the local header only. The
extra field stays under 65,536 bytes: where the padding would pass that,
the excess, at most the field's 6 to 26 fixed bytes, goes as zero bytes
before the local header, which readers find through the central directory. A file whose units all stay stored is
written under its own path, and every zip tool extracts it as the file; a
file with an encoded unit is written as `<path>.oa-units`. Paths are
relative, slash-separated UTF-8 that Windows, macOS and Linux extract as
written: no empty, `.` or `..` element; no backslash, colon, control
character or any of `<>"|?*`; no element ending in a dot or a space; no
element named, before its first dot, `CON`, `PRN`, `AUX`, `NUL`, `CONIN$`,
`CONOUT$`, `COM0` to `COM9`, `LPT0` to `LPT9`, or `COM` or `LPT` followed
by `¹`, `²` or `³`, in any letter case. Every path and entry name is
unique in any letter case. The archive's own zip writer writes Zip64 fields where a size,
an offset or the entry count needs them. `WriteZip` encodes each file twice,
first to learn its entry's size and CRC-32 and the index that precedes the
entries, refuses the archive if the two encodings differ, and gives each
weight file's span record to `SpanRecords` (INF-MS7). `ReadZip` accepts an
archive whose entries are all STORED and unencrypted, whose index is present,
at most 64 MiB, of this format and version, parses with no unknown field and
names every other entry once; whose entry names, and whose paths and entry
names, are unique in any letter case; whose files have such paths, the entry their
units call for, data on the 64 KiB boundary, an index whole by INF-MP1 that
uses exactly the entry's bytes, and tensors inside the file
(`ErrCorrupt`); it reads no file's data. Go's `archive/zip`, bsdtar (`tar`)
and PowerShell's `Expand-Archive` list such an archive and extract every
file entry, Zip64 fields included.

**[INF-MP7] Fill from an archive.** `FillZip` needs OA's span records and
refuses without them. It reads and checks the whole index (INF-MP6) and
refuses an archive holding no file of the object's digest
(`modelfiles.ErrDigest`) before a section exists or a unit is read. It then
fills a section through `FillDecoded` (INF-MS8): each goroutine reads units
through its own `RegionReader`, one positioned read per unit, and decodes
them straight into the section. An `exp-fixed3-v1` unit is decoded with
each escaped value at exponent `b + 7`, which no value inside its chunk's
window has, and `FillDecoded`'s hash pass applies the unit's escapes to
each block just before hashing it; the one value whose two bytes lie in two
spans takes its escape at decode. The decoder records where it found
each escape, its value's index within its chunk, in value order, and each
chunk's first escape, which equals the index the chunk header's bytes 4 to
7 hold; the pass visits those values alone and writes one only where it
carries its chunk's mark `b + 7`, so its cost follows the escapes. A wrong
or altered position list leaves an escape at its mark or a value
unpatched, and the hash refuses those bytes; the list is never trusted
beyond what the proof shows. That pass is the one check of the bytes:
against OA's record of the object when one exists, and against its whole
digest on a first fill. The fill holds each such unit's window bases,
escapes and escape positions until every span holding its bytes has been
passed, then drops them. The bound is the escapes' share: one escape per
eight values at most, one escape byte and a two-byte position each, so at
most 3/16 of the decoded size plus 5 bytes per 4,096 values for bases and
chunk starts. A hostile pack reaches that bound; Qwen2.5-1.5B at 2.94
percent escapes holds 0.7 bytes per sixteen. It
writes no file; `release` is called exactly once, as for `Fill`.

**[INF-MP8] The `exp-fixed3-v1` unit.** BF16 only. Each value's exponent
is coded in 3 fixed bits as an offset into a window of seven exponents
chosen per 4,096 values, and code 7 escapes it: ZipServ's TBE code
(https://arxiv.org/html/2603.17435), in the chunk layout measured on the
NPU in
`GPU-AND-MATRIX-DECOMPRESSION.md` §12.2. A unit is a 64-byte header
(format version 1, type 1 `BF16`, then the value count and the escape
count as little-endian 32-bit numbers at bytes 4 and 8, every other byte
zero), then one 5,696-byte chunk per 4,096 values, the last one padded,
then the escapes. A chunk is a 64-byte header whose first byte is the
window base `b`, at most 248, whose bytes 4 to 7 hold the index of its
first escape in the unit's escape list, the number of escapes the unit's
earlier chunks use, as a little-endian 32-bit number, and whose other bytes
are zero; three 512-byte bit planes; and 4,096 bytes of
`sign << 7 | mantissa`. A decoder can start at any chunk, and NPU tiles can
share one unit's chunks. Value
`v = 512·blk + 64·i + j` of a chunk keeps bit `p` of its code in bit `i` of
byte `64·blk + j` of plane `p`. The exponent is `b + code` for codes 0 to 6;
code 7 takes the next escape byte, in value order. The encoder picks per
chunk the window that covers the most values and refuses a unit with more
than one escape per eight values, which `Pack` then encodes as
`exp-huffman-v1` or stores (INF-MP2). A unit whose bytes contradict this
layout, whose chunk header names another escape index, or whose escapes
do not match its codes, is refused (`ErrCorrupt`).

**[INF-MP9] The pack command.** `modelpack` makes and
reads model archives (INF-MP6) from files on this machine and reaches no
service. It writes one archive of one model file, or of every file under a
folder. It reads each weight file's safetensors JSON header or GGUF tensor
table (`modelpack.ReadTensors`: names, dtypes, shapes and ranges; GGUF
tensors of type F32, F16 and BF16 by element count, every other byte left to
stored runs), maps every file read-only, and packs weight files with the
per-unit choice of INF-MP2 from `--rates` given in GB/s or from rates it
measures first (`modelpack.MeasureRates`: each encoding's decode rate on
synthetic weights in memory, and the read rate of the largest weight
file's volume through the stored fill's unbuffered reader, at most 1 GiB,
within `--seconds`, its clock started once the file is open, each reader
reading at least one 16 MiB block, and a file under 16 MiB read whole once
as one shorter block). Each measurement repeats its decodes, or reads the
file again from its start, until at least 5 ms, or a quarter of
`--seconds` when that is shorter, has passed and the clock has moved.
Every weight file's header is read before any measurement, and an empty or
invalid one refuses the run first. Weight files under 64 MiB in all, none of them
16 MiB, are packed without a measurement, every unit stored, and the run
says so: such an input loads in tens of milliseconds, an encoding would
save a few, and the measurement takes `--seconds`. A measured rate that is
not positive and finite fails the run (exit 1). `measure` on a file under
16 MiB reads it whole once. Every other file is stored as it is. Weight files are
`.safetensors`, `.sft` and `.gguf`; a pickle (`.pth`, `.pt`, `.ckpt`, `.bin`,
`.pkl`, `.pickle`) refuses the whole run before anything is written. The
span record of each weight file is written only into a folder `--records`
names (INF-MS7); the provider host's own first fill records it otherwise.
`info` prints each file's digest, size, bytes kept, and units per encoding;
`verify` decodes every file in memory, checks every span and every file's
digest against the index, and that the archive holds the object `--digest`
names, and writes nothing; `restore` writes the original files byte for
byte, checked as they are written (`modelpack.RestoreFile` and `Restore`):
one file to the path given, several into a folder that must be absent or
empty. No command overwrites a file. The archive `pack` writes and each
file `restore` writes go under a temporary name in the target's folder and
are renamed to the target at the end without replacing anything: Windows
`MoveFileW` without replace, Linux `renameat2` with `RENAME_NOREPLACE`,
macOS `renamex_np` with `RENAME_EXCL`, and on other systems, or where the
filesystem refuses that flag, a hard link and then removal of the
temporary name. The target never holds part of a file, and the temporary
file is removed on any failure. A restore that fails removes every file and folder it made. Exit
status 0 done, 1 a file could not be read or written, 2 usage, 3 refused (a
pickle, a weight file that is not safetensors or GGUF, an archive that fails
verification, an output path where something already stands).

**[INF-MP10] A device beside the CPU.** `FillZipWith` takes an optional
`modelpack.FixedDevice`, a batch decoder of `exp-fixed3-v1` units such as
the NPU decoder in `npudecode`. A batch is a fixed number of
slots of `FixedSlot` bytes (397,376, the largest unit of 512 KiB); the
device decodes each slot's 64 chunks with every value's exponent `b +
code`, so an escaped value leaves at `b + 7` as the fused CPU decode leaves
it. A fill uses the device when the archive file holds units of at most
512 KiB in that encoding, the device reports no error, and no other fill
holds it; otherwise the CPU decodes every unit. Helpers read each batch's
units into a free buffer set and check each one on the CPU as the decoder
checks it, header, window bases, escape indexes and escape count included,
recording its escape positions as the decoder does, before the device
runs; a unit that fails is refused (`ErrCorrupt`) as on
the CPU. While the device decodes one batch the helpers read the next and
copy the previous one into the section; the CPU workers decode every other
unit meanwhile. Escapes stay in the hash pass (INF-MP7), and the one value
split across two spans takes its escape when copied. The proof is the
fill's, unchanged: every span is hashed against OA's record or the whole
digest (INF-MS8), and nothing the device writes counts until it passes.
A device error, a timeout included, hands that batch and every later one
to the CPU, which reads and decodes them again; the error is reported
(`FillReport.DeviceErr`) and logged, and the device refuses every later
run. When the proof refuses a fill the device took part in, the fill runs
once more on the CPU alone; if that fill is proven the device is
distrusted for the life of the process. An archive rewritten between the
two fills can distrust a healthy device that way; that cost is accepted.
`release` is called once either way. `FillReport` gives units per decoder,
device runs, the decode phase's time and the time spent in device runs.
A device is used only after `CheckFixedDevice`, its self-test, decoded
known units with escapes and a padded chunk in every slot of every buffer
set bit-exact against the CPU decode. `npudecode.Open` runs it before it
returns a device. The kit is trusted for code: whatever can write a kit
file runs code in the provider host. `npudecode` therefore refuses any kit
file, `kit.json` included, that identity ID-D2 refuses
(`trustedpath.CheckFile` with `MachineOwners()` and `Account`): a file or
a folder above it up to the volume root owned by anyone but SYSTEM,
Administrators, TrustedInstaller or this account; a right for anyone else
to change, delete or re-permission the file, or to delete, rename or
re-permission a folder on its path; or a reparse point (link, junction or
mount point) anywhere on the path. A kit an installer placed under Program
Files passes, as does one this account built in a folder whose whole path
passes. ID-D2 covers the link and junction checks; it does not compare the
path a handle resolves to, so `npudecode` also refuses a kit file whose
open handle's final path is not its own (a mapped or substituted drive).
It reads `oa_npu_decode.dll`, the design files and
`fixed3.json` through handles that deny writes, checks each against the
SHA-256 `kit.json` pins (and `kit.json` against a pin the caller holds, when
given), and loads the shim by its absolute path with `LoadLibraryEx` and
`LOAD_LIBRARY_SEARCH_SYSTEM32` while its handle is still open, so the
library loaded is the one checked and its imports come from System32. It
refuses to load anything while `XRT_INI_PATH` or `XILINX_XRT` is set or an
`xrt.ini` stands in the working directory or beside the executable, since
XRT reads plugins from that configuration (`ErrXRTConfig`). The shim's four
calls (query, open, decode, close) catch every exception and reach the NPU
driver's `xrt_coreutil.dll` (XRT, Apache-2.0) through `syscall`. The
design's `fixed3.json` must name modelpack's slot and unit sizes. The
decoder runs one batch at a time with a time limit (500 ms; a batch of 64
units takes about 1.2 ms), aborts a run that exceeds it, and never frees
the buffers of a run that timed out. `Close` waits until no fill holds the
device, then frees it, and no fill can claim it after. It is Windows only;
elsewhere `Open` reports `errors.ErrUnsupported`. The design's sources and build scripts
are in `npudecode/native`; built files stay outside OA's trees until the
owner decides they ship (`research/security-reviews/npu-toolchain-iron-1.4.3.md`
condition 5). Measured in
`research/model-locators/GPU-AND-MATRIX-DECOMPRESSION.md` §14. No
provider host passes a device yet, since none fills from an archive.

**[INF-MP11] The `exp-ans-v1` unit.** OA's own static rANS coder
(VISION 2026-10-02; `research/model-locators/ANS.md`), after Duda's
asymmetric numeral systems, Giesen's `ryg_rans` and his interleaved
coders; no third-party code. A unit cuts its 16-bit values into two byte
planes. It is a 16-byte header (format version 1; the layout; the high and
the low plane's modes; then the value count and the two planes' sizes as
little-endian 32-bit numbers at bytes 4, 8 and 12), the high plane and the
low plane. Layout 1 (`BF16`) and 2 (`F16`) rotate each value left by one
bit, as `exp-huffman-v1` does, so the high plane of BF16 is the exponent.
Layout 3 holds a delta's 16-bit words against a base, high and low bytes
as they are, and needs the unit's base values to decode. A plane is raw
(mode 0), one byte for every value (2), rANS with one table (3), or, in
layout 3 only, rANS with one table per context (4), where a value's context
is its base value's BF16 exponent minus the plane's window start, clamped to
the window's contexts, at most 32. A table gives each present byte a
frequency out of 4,096, written as unsigned varints: for each present byte
the count of absent bytes before it and its frequency minus one, then the
count of absent bytes to the end; a context no value falls in has an empty
table. The stream is little-endian 16-bit words: four 32-bit states, low
word first, then the words the states read when they fall below 2^16. Value
i is coded with state i mod 4, each state in [2^16, 2^32), and every state
returns to 2^16 at the end. A plane is coded only where that saves 3
percent of it. A unit whose layout, mode, sizes or tables contradict this,
whose stream runs out or ends with a state other than 2^16 or with words
left, or a layout-3 unit decoded without a base of its length, is refused
(`ErrCorrupt`). A changed raw plane decodes to other bytes, which the span
check refuses as for every unit.

**[INF-MP12] Delta units against a base.** VISION 2026-10-02;
`research/model-locators/DELTA.md`, `ANS.md`. A fine-tune's unit may be
stored as a delta against the same values of a base object: the
`int-diff-v1` transform takes each BF16 value's order-preserving key (the
sign bit flipped for a positive value, every bit for a negative one) minus
the base value's key, modulo 2^16, zig-zag coded, and codes the words as an
`exp-ans-v1` unit in its delta layout, under contexts from the base values'
exponents (INF-MP11). The decoder undoes the difference as it joins the
planes. The index names the base once per file, `base` with its `digest` and
`length`, one level deep, and each delta unit its `transform`
(`int-diff-v1`) and `base_offset`, the decoded offset of its `length` bytes
in the base (any byte; tensors need not be aligned). A reader refuses
(`ErrCorrupt`) a delta unit with another transform or encoding, an odd
length, bytes outside the base, or no base in its index; an index whose base
digest is malformed or whose length is not positive; a base offset on a unit
with no transform; and a base named by an index that holds no delta unit.
`PackDelta`, and `WriteZip` for a source that names a `DeltaBase`, tries each
unit of a BF16 tensor the base holds by the same name, dtype and length as a
delta and keeps it where it is smaller than the unit's other choice and saves
3 percent; a delta is chosen for disk and download size. Every delta unit is
decoded against its base before it is written. The base must be a proven
object before any delta unit decodes. `UnpackWithBase`, `RestoreWithBase` and
`RestoreFileWithBase` take a `ProvenBase`, which `ProveBase` makes by hashing
the whole base once and retaining the caller's byte slice without copying it.
This is a borrowed in-process value, not an owned immutable snapshot: its
caller must keep the backing storage alive and unchanged through every alias,
from entry to `ProveBase` until every operation using the result has returned.
That includes calls using copied `ProvenBase` values concurrently. The digest
is not rechecked as bytes are read. Mutating the source after proof violates
this precondition and can stale the identity claim, even when changed bytes
are outside the delta units' read set and the fine-tune output remains
correct. `FillZipWith` takes the base's section in
`FillOptions.Base`, proved when it was filled (INF-MS2, INF-MS8). Before a
section is made or a unit read, a delta object with no base is refused
(`ErrBase`), and one whose base is another object by digest or length is
refused (`modelfiles.ErrDigest`). The fill reads the base section through a
loan of its own, mapped read-only, so the base stays in memory until the last
delta unit is read; the loan's end frees a base no engine holds, as any
section is freed at its last loan's end (INF-MS5). A caller that uses the
base section after a fill, or fills further deltas from it, therefore keeps a
loan of its own on that section for as long as it does; nothing else keeps it
until the residency broker owns base residency. The fine-tune is proved as
every fill is: span by span against OA's record of it, or whole against its
digest on a first fill. A base matching the index's digest but differing from
the base used when packing can still produce the same fine-tune when those
differences are outside the delta units' read set. When referenced bytes
change decoded output, the trusted fine-tune proof refuses it; changed base
exponent contexts may cause `DecodeDelta` to refuse earlier with `ErrCorrupt`
naming both causes ("or not the base this delta was made from").
`modelpack --base <file or archive>` packs one
weight file against a base, given as a safetensors file or as an archive
holding one weight file that is not itself a delta. `verify` and `restore`
take the same `--base`, refuse a delta without it and refuse another base
(exit 3). `info` names each file's base. A CLI base stays unchanged and
available through the last operation that reads it; on Windows its source
file stays open with writes and deletes denied, and on Unix it is read into a
private copy. The CLI releases that mapping or buffer only after the last
use. The fill path takes an explicit section loan and a read-only mapping
until the last delta unit has been decoded. The decoded fine-tune still has
its own proof: recorded 16 MiB spans, or its whole digest on a first fill.
Restore checks the whole digest at completion against the index; as with any
pack, that alone verifies the pack's claimed bytes, and the caller compares
an independently known digest when identity matters (INF-MP1). A wrong
immutable base whose digest is supplied by a caller is rejected by the
fine-tune's trusted span or digest proof if it changes decoded output. On a
failed restore, output may be partial and the caller must discard it; the CLI
restores to a temporary file and removes it on failure.

`ModelArchive.Bases()` reports base dependencies, but no archive store or
inventory consumes it today; modelpack only builds and reads archives. The
future shared archive-store removal/retention path is the single meeting
point that must retain a base while a stored delta names it and release the
dependency when that archive is removed. A delta whose base is absent is
fetched as the whole fine-tune (VISION 2026-10-02 (3)); there is no current
store-level retention behavior to qualify. The independent caller that uses
a base section across fills keeps its own loan until done. Costs are bounded
per unit (2 MiB decoded; for exp-ans-v1 at most 16 bytes of packed overhead)
and Restore decodes in a 256 MiB window (INF-MP1, INF-MP9); there is no
contract total-object-size cap. ProveBase's one full SHA-256 pass is O(base
length), but it adds no base-sized copy and no per-unit whole-base hash.
