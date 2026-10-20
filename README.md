# Provider kit

The provider kit is the code every OA provider of a local engine shares, so
an engine kind is written once and supervised the same way everywhere. A
provider adds its engine's launch, its pins and its protocol; the kit
supervises the process, refuses loads in one vocabulary, proves model files
in place and runs video@1 operations. The rules are in
[CONTRACT.md](CONTRACT.md) (page code `KIT`).

**What's new here:** The model host and the PyTorch provider start, supervise
and stop their engines with the same code: on Windows the engine runs in a job with a
memory limit and ends as one process tree, every end leaves one exit record,
and a model file is proved by size and SHA-256 where it lies and never
copied.

| Package | What it owns |
|---|---|
| `engineproc` | The engine process: Windows job with a memory limit, clean environment, bounded log, exit record, device-lost line, the AppContainer it starts in, a read-only handle lent to it, and `Owned` for the resource kit's `holder` |
| `sandbox` | The engine's Windows AppContainer with no network: one profile per engine kind, grants applied once at install with a manifest and access-list snapshots, the launch check, and removal that restores every access list |
| `refusal` | The typed refusal and the words of a refused load |
| `modelplace` | Pinned model files found, proved in place and recorded; OA's model folders and `models protect` |
| `videoops` | The video@1 operation table: admission, queue, retention, cancel, GPU admission |
| `quarantine` | A provider's `gpu list` and `gpu clear` |
| `runtimeprog` | The runtime programs a provider's endpoint admits |

Users: the model host (`abstraction-provider-modelhost`) and the PyTorch
provider. A new provider composes the kit with `holder` (its card lease) and
`gpufault.ForProvider` (its GPU fault handling).

```go
proc, err := engineproc.Start(engineproc.Spec{Program: program, Args: args, Env: env,
	Output: log, MemoryLimit: limit, Engine: "my-engine", Model: set, Provider: "myproviderd"})
if err != nil {
	return nil, err
}
owned := &engineproc.Owned{Name: set, Process: proc, ReadyWithin: 2 * time.Minute,
	Probe: myProbe, Sockets: mySocketRule, SocketWord: "engine:network-socket"}
// owned is the holder.Owned a holder.Component's Start returns.
```

An engine that runs contained gets its sandbox from Setup once, and each
launch checks it and starts inside it:

```go
// Setup, once per install or update:
s, err := sandbox.Begin(manifest, "oa.engine.myprovider.mykind")
err = s.Grant(sandbox.Grant{Path: engineDir, Access: sandbox.ReadExecute},
	sandbox.Grant{Path: cacheDir, Access: sandbox.Modify})
err = s.Commit()

// Each launch:
launch, err := sandbox.Prepare(manifest, needs) // refuses with the reason
proc, err := engineproc.Start(engineproc.Spec{..., Container: launch.SID})
// launch.Close() once proc has exited; lend to the original child with
// proc.Lend(f), or to a proven same-job contained descendant with
// proc.LendTo(f, descendantPID).
```

An application session uses `ApplicationProfileName` for its own `oa.app`
profile. `ProfileSID` validates and derives its SID without creating a profile
or granting access. A resource owner that obtained a human folder approval uses
`Setup.GrantBound(grant, approvedFileKey)`; each opened ACL handle must still
name that identity before any snapshot or grant. The ordinary engine `Grant`
entrypoint and install-before-launch/shared-launch-lock lifecycle stay the same.
`Modify` permits reading, running, creation, edits and deletion throughout a
folder and its descendants. The application keeps its approval/effect record
outside every guest-granted tree and removes its grants after confirmed exit.

The provider kit packages inside `abstraction-inference/go/providerhost`
(`enginelaunch`, `enginefiles`, `devicelost`, `sdcpp`, `videoenc` and the
rest) move here in a later step (architecture review 2026-10-02 D4).
