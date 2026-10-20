# Provider kit contract

The provider kit holds the parts every local-engine provider shares: the
engine process it starts and supervises, the typed refusal of a load, the
model files it proves where they lie, and the video@1 operation table. A
provider composes them with the resource kit's `holder` and `gpufault`; the
kit carries no engine of its own and no provider's vocabulary beyond the
words below.

Binds: [inference.thrift](https://github.com/openabstractions/abstraction-inference/blob/main/inference.thrift)

## KIT-P1 — One owned engine process

`engineproc.Start` MUST start an engine only from the environment its caller
built; an empty environment is refused and nothing is inherited. On Windows
the engine MUST be assigned to a job with kill-on-close before its code runs,
with the caller's per-process memory limit, and its exit MUST read as ended
only once every process of the job is confirmed gone. Outside Windows the
engine is a direct child and a memory limit is refused.

## KIT-P2 — The engine's log

An engine's output MUST be kept only in a log of the provider's private
state, bounded per file with one earlier file kept. It carries prompts and
request bodies and MUST NOT reach a caller.

## KIT-P3 — One exit record per end

Every end of an engine MUST produce one exit record naming the engine, the
model it was started for, its process id and what its supervisor saw: the
code or signal, the run time, the memory peaks and whether the job's limit
refused an allocation. The record goes to the provider's exit hook and, when
the provider owns the stream, as the last line of the engine's log.

## KIT-P4 — The device-lost line

A provider that listens for an engine's own device-lost line reads it on the
engine's output and binds it to the process that printed it. The line counts
as a lost device only once that engine exits or fails its own readiness
twice within `DeviceLostConfirm`; it is then reported to the provider's GPU
fault handling (resource CONTRACT.md RES-G1).

## KIT-P5 — The engine as a holder component

`engineproc.Owned` is a `holder.Owned`: its readiness is the engine's own
probe within the provider's budget, then the engine's socket rule (inference
INF-EL2). The socket rule is checked again every watch interval while the
engine runs; a broken rule stops this engine by its own process. A server on
the engine's socket outside its tree refuses readiness with `engine:peer`.
A probe whose every connect for `UnreachableFor` (3 s) failed with an
error a starting engine never gives (enginehttp `ErrEngineUnreachable`, inference
INF-EL2) refuses readiness with `engine:unreachable`, naming the system
error, instead of waiting out the provider's budget.

## KIT-P6 — A stop targets one process

A stop MUST end only the process tree the provider started and report it
stopped only once that tree's exit is confirmed.

## The engine's sandbox

`sandbox` runs an engine OA launches inside a Windows AppContainer with no
network capability (research/engine-sandbox/REPORT.md). The container is the
engine's network containment: it reaches no TCP, DNS or UDP, its children
included, and needs no administrator. Process creation inside it is the
facade's (`supervision.Options.Container`, FAC-P3); this package owns what
the container is given, for every provider whose engine runs contained.

## KIT-S1 — One profile per engine kind

An engine kind's profile is `oa.engine.<provider>.<kind>`, with `.<label>`
after it for a second installation of the same kind; each part is lower-case
letters, digits and hyphens, and the name is at most 64 characters. The
container has no network capability: no `internetClient`,
`internetClientServer` or `privateNetworkClientServer`. Its one capability is
the facade's `ContainerCapabilitySID` (FAC-P3, a TEMPORARY EXCEPTION, KIT-S7),
which reaches only what an access list names it for. LPAC, which also refuses `socket()` itself, is
not used: an engine whose libraries import asyncio, as PyTorch's do, cannot
start there.

## KIT-S2 — Grants are applied once, at install or update, and recorded first

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

## KIT-S3 — A launch checks the sandbox and holds it

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

## KIT-S4 — Removal restores every access list and deletes the profile

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

## KIT-S5 — A stopped install is found and removed

An install stopped after any step, as a crash stops it, leaves the phase
`installing`, which refuses every launch, and a manifest naming every path it
touched, so `Remove` takes it all back. `Profiles(prefix)` lists the profiles
the system records for this account, a left one included.

## KIT-S6 — Files outside the grants are lent, never granted by a launch

A provider gives a contained engine a file it may not open by path, such as a
model the person adopted in place, by `engineproc.Process.Lend`: a read-only
handle duplicated into the running engine (facade FAC-P4), which reads the
bytes through it and cannot write them. Outside Windows every call of
`sandbox` is refused (`ErrUnsupported`) and `engineproc.Start` refuses a
container.

## KIT-S7 — Ancestors get attributes, never contents

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
`TestAGrantRefusesALink`, `TestContainedEngineBoundaries` (TCP, DNS and UDP
blocked, a child's TCP blocked, the state folder written, Documents and an
ungranted file denied, an OA pipe that admits the container answers, a lent
handle reads and does not write), `TestContainedLaunchCost`,
`TestAStopMidLaunchLeavesTheSandboxWhole`.

## KIT-R1 — The typed refusal

A refusal carries a word the caller reads and a detail the provider's log
keeps. Neither carries engine text.

## KIT-R2 — The words of a refused load

A load the lease refused reads the resource kit's lease word
(`holder.Failure.Word`); a refusal the load made itself reads its own word;
any other failure reads `unavailable`. A GPU fault refusal keeps the Guard's
word (`gpu:standby`, `gpu:resetting`, `gpu:quarantined`). A video or image
whose engine could not load and named no word fails `engine:unavailable`.

## KIT-M1 — Model files proved where they lie

`modelplace.Place` MUST find each pinned file through the provider's
searches, prove it in place by size, SHA-256 and, when pinned, its Git blob
SHA-1, and record where it lies. It copies nothing (VISION 2026-10-01). A
path through a junction or symbolic link is refused. A store search reads
the provider's `StoreFiles`, the stores' names and a whole stored file's
path; the kit imports no provider, and a provider that reads stores adapts
its own source.

## KIT-M2 — The places record

A places record names each pinned file once, with its absolute path and the
owners it may be trusted under: the machine's owners and this account alone.
A record naming another owner, a file of no pin, or missing one is refused;
a refusal names the command that writes the record.

## KIT-M3 — OA's model folders

`modelplace.AdoptModelFolder` makes a folder directly under OA's models root
a model folder of this account (identity ID-D3) and leaves every other folder
as it is.

## KIT-M4 — No copy, no download

The kit downloads no model and copies none; a provider's Setup stages files,
and the kit proves them where they lie.

## KIT-V1 — Admission and the queue

`videoops.Table.Start` admits a video for its caller (inference INF-G5): a
request_id the caller holds names its operation again; one model; a request
the kind refuses is refused before it is queued; a quarantined fingerprint
is refused at once (resource RES-G3). The table runs one video at a time and
refuses `queue:full` and `operations:full` past its bounds.

## KIT-V2 — The model set's bounds

A request is resolved to the set's preset for size auto and rate zero, its
frames aligned to 4n+1; a value outside video@1 is invalid, and one outside
the set's sizes, frames, rate or first-frame use is unsupported_feature.

## KIT-V3 — The first frame

A first frame is read from the provider's own store by digest and becomes the
provider's own PNG of the exact size; a digest the store does not hold is
`content:unknown`, and nothing is generated from the prompt alone.

## KIT-V4 — Cancel

A queued video leaves the queue; a running one is stopped by its kind, which
reads the operation's cancel.

## KIT-V5 — Retention

An ended operation and its request_id stay held for the retention, at least
24 hours; its result is removed from the store with it unless another held
operation delivers the same digest.

## KIT-V6 — The GPU around a video

A video waits while the machine is in Modern Standby or the driver is coming
back from a reset, holds the wake lease while it runs (resource RES-G5), and
reads `gpu:reset` and quarantines its fingerprint when its engine is lost in
a recorded reset; any other loss reads `engine:lost`.

## KIT-Q1 — The GPU quarantine commands

A provider's `gpu list` prints each request its GPU fault handling paused
after a driver reset, with its key, strikes, until when and what it
dispatches, and names a damaged quarantine file with where it was moved;
`gpu clear KEY` (or `--all`) lets a paused request run again (resource
RES-G3). Only a person runs them.

## KIT-E1 — The runtime programs a provider admits

A provider's endpoint admits the runtime programs its installation names: the
installed runtime beside the provider (both forms on Windows), or the clean
absolute paths `--runtime-program` names. The connecting process never
names them.
