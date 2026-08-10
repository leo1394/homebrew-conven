# Conven

**English** | [简体中文](README-ZH.md)

`conven` is a focused launcher for local microservice development. It starts only
the services involved in the current change, keeps the remaining dependencies
reachable through the development registry, and collects the local session logs
under `<workspace>/.loom/runtime/current`.

```text
Convening local services: user-svc, order-svc, payment-svc
```

Conven itself is written in Go, but the services it launches are not limited to
Go. Each service declares prepare, build, and run commands as argv arrays in the
workspace manifest, so Conven can run Go, Java, Node.js, or any other service that
can be started by a local command. Automatic repository discovery is narrower:
v1 uses an extensible `RepositoryAnalyzer` seam to recognize Go main modules at
the repository root or under `go/`. Other layouts remain fully supported when
declared manually in the manifest.

## v1 scope

- Starts only services named explicitly or selected in the interactive picker.
- Restarts only changed or exited services from the current session, while
  keeping unchanged services and the existing cluster connection running.
- Injects `dependencies.<service>.localEnv` or rewrites the matching YAML
  binding to a local address for selected dependencies. For unselected
  dependencies it injects `remoteEnv` or preserves remote discovery settings.
- Can read configuration from a service repository or Apollo, generate
  `.loom/runtime/current/configs/<service>` outside the source tree, and overlay
  server-port and dependency-routing patches from a policy.
- Can establish local-to-cluster connectivity through `ktctl connect`.
- Records process metadata and per-service logs for later inspection and
  shutdown. Health checks run only during startup; they are not persisted as
  continuous monitoring.
- Does not provide `ktctl exchange`, Mesh, or Preview behavior. Cluster traffic
  is not routed back to local services.
- Does not infer business dependencies or safely register local services in a
  remote registry. Those behaviors must be configured explicitly in the
  manifest.
- A passing health check proves only that a local process or endpoint is ready.
  End-to-end behavior still requires the project's own smoke tests.

When starting a service locally, use `localEnv` to disable service registration
so traffic from other development environments cannot reach a personal process.
Use `remoteEnv` to retain remote discovery for dependencies that are not started
locally.

## Requirements

- macOS or Linux
- Go 1.23 or later when building from source
- Homebrew when installing the Formula
- `ktctl` only when using the `ktctl` connection driver
- `sudo` only when a connection sets `connection.sudo: true`

## Installation

Add the tap once, then install the stable Formula with its short name:

```bash
brew tap leo1394/conven
brew install conven
```

To install the development version from `master` instead of the latest stable
tag, use:

```bash
brew install --HEAD conven
```

To build directly from a Conven source checkout:

```bash
go build -o /tmp/conven ./cmd/conven
/tmp/conven --version
```

The Formula installs Bash, Zsh, and Fish completions. `__completion` is an
internal command used by the Formula and normally does not need to be invoked
directly.

The rename changes the product, executable, repository, and Formula names. For
workspace compatibility, Conven continues to use `.loom`, `loom.yaml`,
`~/.loom/config`, `LOOM_*`, and the existing `loom` user-state subdirectory.

## Design: generic capabilities and declarative project rules

Conven separates reusable mechanics from project-specific conventions:

- Generic capabilities cover read-only repository analysis, service planning,
  connection management, repository or Apollo configuration input, YAML
  materialization, process lifecycle, and logs.
- Declarative project rules live in the workspace's sole canonical manifest,
  `<workspace>/.loom/loom.yaml`. `policies` describe company or framework
  conventions, `services` describe ports, dependencies, bindings, and runners,
  and `environments` describe target environments and connections.

There is no separate `.loom/policy.yaml`. Most changes to company conventions,
ports, field names, or local/remote routing should change declarations. A code
extension is needed only when a repository layout, configuration protocol, or
materialization behavior cannot be expressed by the existing analyzer,
configuration-source, and materializer seams.

## Quick start

Initialize the current directory as a Conven workspace, inspect the repositories
detected from its immediate children, then fill in the application-specific
ports, environment variables, and dependency routing:

```bash
mkdir -p /path/to/workspace
cd /path/to/workspace
conven init
conven services --list
conven policy --edit

conven doctor
conven services --start --dry-run user-svc order-svc payment-svc
conven services --start user-svc order-svc payment-svc
```

The service names above are from the fallback example. When repositories are
detected, pass names shown by `conven services --list` instead.

On first initialization, `conven init` scans only immediate child directories that
are Git repositories. v1 includes two `RepositoryAnalyzer` implementations:
`go-root-module` checks `go.mod`/`main.go` at the repository root, and
`go-subdirectory-module` checks `go/go.mod`/`go/main.go`. Both require
`package main` and a module-path basename equal to the repository directory
name. A match produces the corresponding `runner.workdir`,
`[go, build, -o, "${artifact}", .]` as `runner.build`, and `["${artifact}"]` as
`runner.run`. The analyzers also conservatively classify go-zero HTTP/RPC server
kind and extract RPC client binding candidates from explicit YAML tags. They
leave ambiguous facts unknown instead of guessing.
`RepositoryAnalyzer` is currently a code-level extension seam; v1 does not load
third-party analyzers dynamically by a name in the manifest.

A work-in-progress repository whose corresponding `main.go` cannot yet provide a valid
`package main` clause, or whose `go.mod` has no module directive, is reported as
skipped. It does not prevent other repositories from being discovered.

If no repository is a strong match, `init` falls back to the template embedded
from [`examples/application.yaml`](examples/application.yaml). Running `init`
again is safe: an existing manifest is reported but never overwritten. Initial
publication is atomic and no-replace, so a manifest created concurrently wins
instead of being overwritten. Use `conven services --registry` after the set of
child repositories changes. Before first runtime use, Conven adds `/runtime/` to
`.loom/.gitignore` without replacing existing ignore rules.

Initialization follows a deliberately narrow sequence:

1. Resolve the workspace directory and validate its `.loom` boundary.
2. Read only immediate child Git repositories.
3. Run the built-in `RepositoryAnalyzer` implementations.
4. Record only facts the analyzers can prove: path, runner, kind, and discovery
   or binding candidates.
5. Atomically create `.loom/loom.yaml` without replacing a concurrent file. Only
   `init` may use the embedded example when no repository is a strong match.
6. Do not contact Apollo, create runtime state, build or start services, or write
   into child repositories.
7. Use `conven policy --edit` to declare project rules scanning cannot infer, or
   import a complete candidate from a project-local generator with
   `conven policy --import <yaml-file> --edit`. Then run `doctor`, a start dry-run,
   and the actual start.

Repeating `init` never overwrites the canonical manifest and is not a restore
operation.

`services --start` starts services in the background by default. To enter the
live log dashboard immediately, use the boolean `--tail` switch:

```bash
conven services --start --tail user-svc order-svc
```

To inspect the resolved plan without starting anything:

```bash
conven services --start --dry-run user-svc order-svc
```

## Editing, importing, or resetting project rules

`policy` is a command name, not a second policy file. All three primary actions
target the complete canonical `<workspace>/.loom/loom.yaml`:

```bash
conven policy --edit
conven policy --import ./generated-loom.yaml
conven policy --import ./generated-loom.yaml --edit
conven policy --reset
```

`--edit` copies the current manifest to a private temporary draft and opens the
first configured editor from `LOOM_EDITOR`, `VISUAL`, or `EDITOR`, falling back
to `vi`. Editor commands may contain arguments, for example
`LOOM_EDITOR="code --wait"`; graphical editors must wait for the file to close.
Conven publishes the draft's exact bytes only after the editor exits successfully,
strict schema and semantic validation pass, and a best-effort pre-publication
check detects no change to the canonical source snapshot. On an editor failure,
invalid YAML, unknown field, or detected concurrent edit, Conven does not publish
the draft or overwrite the concurrently observed manifest. A rejected changed
draft is kept with mode `0600` under `.loom/backups/`, which Conven adds to
`.loom/.gitignore` as `/backups/`.

The commit step takes a non-blocking advisory lock on the current manifest
inode, then rechecks same-file identity and source bytes before rename. This
serializes cooperating Conven writers. An arbitrary external writer does not have
to honor that lock, so conflict detection against other tools remains
best-effort across the final check-to-rename interval.

`--import <yaml-file>` reads a complete local Conven v1 manifest and publishes its
exact bytes as the entire `.loom/loom.yaml`. A relative source path is resolved
from the invocation cwd, not from the workspace root. Import never edits or
moves the source file, and it does not merge the candidate with existing scan or
manual fields. A changed existing target is first backed up with mode `0600`
under `.loom/backups/`; byte-identical content is a no-op. With `--edit`, Conven
seeds a private draft from the imported bytes, opens the editor, and leaves the
source untouched. This also allows an editor to repair an initially invalid
candidate before publication. In either mode, only the final candidate is
published after strict schema and semantic validation and the normal conflict
checks pass.

Successful import validation proves that the file is a valid Conven v1 manifest;
it does not prove that ports, dependency routes, Apollo/Consul endpoints,
credentials, source paths, or service commands work on this machine. Always
follow an import with `conven doctor` and
`conven services --start --dry-run SERVICE...` before a real start.

`--reset` is an explicit destructive reset operation. It rebuilds
the entire manifest from current read-only analysis of immediate child Git
repositories. It is not a rollback, merge, or alias for `services --registry`.
Before replacing an existing manifest, Conven saves its exact bytes with mode
`0600` under `.loom/backups/` and prints the backup path. It can rebuild a
missing or invalid manifest when the real `.loom` workspace boundary still
exists. If no supported repository is found, it fails without changing the
canonical manifest and never falls back to the embedded example.

> **Warning:** scan reset cannot preserve or recover `workspace.policy`,
> `policies`, `environments`, ports, dependency topology or binding assignments,
> `env`, health checks, service patches, manual runner changes, or YAML comments.
> Analyzer bindings are candidates, not a reconstructed dependency graph.

After a scan reset, re-declare and verify the project rules before starting:

```bash
conven policy --reset
conven policy --edit
conven doctor
conven services --start --dry-run SERVICE...
```

| Command | Purpose | Treatment of manual rules |
| --- | --- | --- |
| `conven init` | Create a missing manifest | Never overwrites an existing manifest |
| `conven policy --edit` | Edit a validated temporary draft | Preserves everything the user does not change |
| `conven policy --import <yaml-file> [--edit]` | Publish a complete local v1 manifest, optionally after editing a private draft | Replaces the whole manifest; backs up but never merges or modifies the source |
| `conven services --registry` | Conservatively merge newly scanned facts | Existing non-empty manual fields take precedence |
| `conven policy --reset` | Rebuild the whole manifest from scan facts | Discards manual declarations; restore them from the printed backup or edit again |

### Generated and manually confirmed fields

v1 has only the central `.loom/loom.yaml`; Conven does not create or read distributed
`.loom/service.yaml` or a separate Policy Profile file. A standardized project
configuration commits service, policy, and environment profiles together in the
central manifest.

| Field | Repository scan | Project-local generator | Manual confirmation still required |
| --- | --- | --- | --- |
| `version`, `workspace.name` | `version: 1`, directory basename | May apply a project default | Correct candidate/workspace match |
| Service name/path | Immediate child Git repositories | May enforce a reviewed service inventory | Non-standard layouts, renamed or missing services |
| `kind`, `discovery` | Unambiguous HTTP/RPC kind, analyzer, binding candidates | May resolve known project conventions | Ambiguous kind and the real dependency behind each binding |
| Runner | Standard Go root or `go/` module workdir/build/run | May apply reviewed project runners | Special argv, prepare, artifact, or `runWorkdir` |
| Policy/config/routing | Not generated | May encode reviewed framework and routing defaults | Bootstrap fields, registration disabling, and routing semantics |
| Environment/connection | Not generated | May emit a credential-free connection skeleton | Cluster, namespace, context, network entry, and authentication |
| Ports | Not generated | May apply a reviewed project port table | Actual listeners and local conflicts |
| Dependencies | Binding-name candidates only; no graph | May map reviewed project dependencies | Complete business graph, target ports, and remote-preserve choices |
| Patches/health | Not generated | May apply reviewed project defaults | Side effects, protocol-specific health, and smoke tests |
| Kubeconfig/secrets | Not generated | Must not hard-code them | Configure through `conven config`, environment, or an external credential system |

Repository analysis and a project-specific generator have different authority.
The analyzer emits only source facts it can prove, such as path, standard runner,
kind, and binding candidates. A generator may combine those facts with explicit
project defaults for ports, dependency targets, policy drivers, environment
profiles, patches, and health checks, but those defaults remain manually
reviewed policy rather than newly proven scan facts. Its output is therefore a
complete candidate, not a partial overlay.

The review-once workflow for such a generator is:

```bash
./generate-project-loom-policy              # writes a complete local v1 candidate
conven policy --import ./generated-loom.yaml --edit
conven doctor
conven services --start --dry-run SERVICE...
```

After review, commit the canonical `.loom/loom.yaml`, not a claim that the
generator made every field automatic. Re-run the generator/import flow only
when the repository or project defaults change.

Import does not use field-level precedence: complete local candidate → optional
`--edit` changes → strict validation → whole-file publication. Run
`conven services --registry` afterward to conservatively backfill empty analyzer
metadata; it still cannot infer ports or a dependency graph.

Once maintainers have reviewed and committed a standard `.loom/loom.yaml`, that is
the project's one-time confirmation. Developers should not re-import a generated
candidate after every clone; they configure machine-local
kubeconfig/credentials, run doctor, and start. Maintainers regenerate/import or use
registry/edit only when repository facts or project rules change.

## Refreshing discovered services

Run discovery from the workspace root or any of its descendants:

```bash
conven services --registry
conven services --registry --prune
```

`services --registry` resolves the nearest workspace from the current directory,
but always scans immediate child Git repositories of that workspace root. It
uses the same `RepositoryAnalyzer` seam as `init`. Newly matched repository paths
are added to `services`; for a service already associated with the same path,
Conven only backfills a missing `kind` or the entire missing `discovery` metadata.
Manually populated non-empty fields, runners, and YAML comments take precedence
and are not replaced. Entries missing from the scan are retained by default.

`--prune` synchronizes missing paths in the direct-child discovery scope by
removing entries whose repository no longer exists or is no longer a directory.
An existing but unsupported repository is not pruned. The merged manifest is
validated before publication. Pruning therefore fails without changing the
manifest when a retained service still declares a dependency on a removed
service; update the dependency first and retry. Because the manifest does not
record whether an entry was generated or handwritten, review `--prune` whenever
manually managed services also point at direct child directories.

For an update, `services --registry` decodes both the typed manifest and editable
YAML tree from one source byte snapshot. It strictly decodes and validates the
final YAML again before publication, then requires the decoded typed manifest
to equal the already validated candidate. This prevents a YAML `<<` merge key
from making a pruned service reappear semantically even though its explicit
mapping entry was removed.

Immediately before rename, `services --registry` makes a best-effort check that
the path still identifies the same file and its bytes still equal the source
snapshot. A detected conflict aborts publication with a retry error. This is
not a linearizable compare-and-swap against arbitrary external writers: an edit
in the narrow interval between that check and rename may still be replaced. A
symbolic-link `.loom/loom.yaml` is rejected because atomic replacement would
otherwise break the link rather than update its target.

Discovery intentionally does not infer ports, a complete dependency graph,
`env`, Apollo credentials, company policies, or cluster connection settings. A
repository being discovered means only that Conven can derive its entry point and
a small amount of static descriptive metadata. It does not prove that the
service can join an end-to-end development flow without further manifest
configuration and project smoke testing.

## Interactive PathPicker

When `conven services --start` receives no service arguments, it opens the built-in
PathPicker in a TTY. Candidates come only from the manifest's `services` map;
PathPicker never scans or guesses repositories. Repository scanning occurs only
during `init` or an explicit `services --registry` action.

| Key | Action |
| --- | --- |
| `j` / `k`, `↓` / `↑` | Move the cursor |
| `f` | Select the current service; press `f` again to clear it |
| `F` | Toggle the current service and move to the next item |
| `a` | Toggle between all selected and none selected |
| `Enter` | Open the confirmation screen when at least one item is selected |
| `q` / `Esc` / `Ctrl-C` | Cancel |

The confirmation screen shows the complete selection:

```text
Convening local services: user-svc, order-svc, payment-svc
```

Startup proceeds only after entering `y` or `yes` (case-insensitive) and
pressing `Enter`. Any other answer cancels. Pressing `Enter` with an empty
selection stays in the picker. A non-TTY, an empty candidate list, or a read
error returns an error; an explicit user cancellation exits successfully. None
of these cases starts services implicitly.

## Restarting changed services

`conven services --restart` without service arguments examines every service
selected in the current successful session. It restarts a service only when its
process has exited, its source-tree fingerprint has changed, or its resolved
plan fingerprint has changed since that service's most recent successful start
or restart. In a Git worktree, the source fingerprint covers tracked files and
non-ignored untracked files under the service directory; outside Git it covers
the directory contents except `.git` and `.loom`. The plan fingerprint covers
the resolved prepare/build workdir and run workdir, artifact, declared ports,
command argv, environment, health-check configuration, and the resolved
policy/config materialization plan. Changing only `runner.runWorkdir` or policy
routing is therefore enough for an argument-free
`services --restart` to select that service.
Remote Apollo content is not itself part of the local fingerprint. If only that
remote content changes, pass the service name explicitly to force a restart and
refetch its configuration.

Pass service names to force those current-session services to restart even when
their fingerprints are unchanged:

```bash
conven services --restart
conven services --restart user-svc order-svc
conven services --restart --tail user-svc
```

Restart uses the current session's environment,
`.loom/runtime/current`, connection, and per-service log paths. It does not
reconnect or interrupt unchanged services. It rematerializes configuration and
runs prepare/build only for restart targets; artifacts, configurations, and logs
for unchanged services remain untouched. The existing target log receives a
restart marker before new output is appended. Materialization, prepare, and
build steps for all targets finish before Conven stops any target. Conven also
verifies every target's resolved run workdir at that point; a missing
or non-directory run workdir aborts the restart while the old processes are
still running. Fingerprints are captured before those steps and committed only
after a successful start, so an edit made during a build remains pending for
the next `services --restart`.

## Tail dashboard

`--tail` is a boolean switch supported by `services --start`,
`services --restart`, and `services --logs`; it does not accept a line count. In
a usable interactive TTY at least 20 columns by 4 rows, it opens a full-screen
dashboard after startup or restart completes. A fixed banner shows the workspace
and environment, the current LAN IPv4 address, and each started service with the
named port values snapshotted from its manifest. The remaining rows aggregate
the last 80 lines from each selected log and continue scrolling as new output
arrives.

Press `q` or `Ctrl-C` to leave the dashboard. This only detaches the viewer;
the local services continue running in the background. The dashboard redraws
after terminal resize events and removes ANSI and other terminal control
sequences from service output before rendering it.

If either input or output is not a TTY, including when output is redirected or
piped, or if `TERM=dumb` or the terminal size is unavailable or smaller than
20x4, `--tail` falls back to a plain continuous text stream. Each line is
prefixed with `[service]`, and no dashboard control sequences are emitted.

The displayed ports are the manifest declarations captured when each service
was started or restarted, not a probe of currently listening sockets. A LAN
address and declared port appearing together in the banner does not guarantee
that the process is bound to that interface or reachable at that endpoint.

## CLI

```text
conven init
conven config [--global] [--list|--unset] [key] [value]
conven policy --edit
conven policy --import <yaml-file> [--edit]
conven policy --reset
conven doctor [flags]
conven services --list
conven services --registry [--prune]
conven services --status
conven services --logs [--tail] [service...]
conven services --start [flags] [service...]
conven services --restart [flags] [service...]
conven services --stop [--force] (<service...>|--all)
conven services --stop-all [--force]
conven --version
```

`policy` requires exactly one primary action first. `--edit` after `--import` is
that action's optional modifier, not a second primary action.
Import requires exactly one source path; the other actions accept no positional
arguments. All locate the nearest real `.loom` boundary without requiring the
current manifest to parse first, so edit can repair invalid content and
import/reset can recreate a missing file. Candidate validation failure,
a symbolic link, or a detected publication conflict prevents publication.

The action flag must be the first argument after `services`, and exactly one of
`--list`, `--registry`, `--status`, `--logs`, `--start`, `--restart`, `--stop`,
or `--stop-all` is required. The former top-level `list`, `discover`, `status`,
`logs`, `start`, `restart`, and `stop` commands have been removed and return a
usage error.

`services --start` supports:

```text
--env NAME             defaults to dev
--dev                  equivalent to --env dev
--test                 equivalent to --env test
--kubeconfig FILE
--context NAME
--namespace NAME
--tail
--dry-run
--skip-build           skips build only; fresh-start default artifacts are not reusable
--skip-verify
```

`--dev` and `--test` are available on both `services --start` and `doctor`. A
profile must still be declared under `environments` in the manifest. Combining
a shortcut with the same `--env` value is allowed; conflicting shortcuts or
values fail before workspace startup.

`services --restart` supports:

```text
--tail
--skip-build           skips build and reuses artifacts from runtime/current
--skip-verify
```

`services --restart` intentionally has no `--env`, `--kubeconfig`, `--context`,
or `--namespace` flags because it reuses the current session and connection.

Every `services --start` is a fresh start: after confirming that no active or
untrusted saved process remains, Conven safely resets
`.loom/runtime/current/{artifacts,configs,logs}`. Therefore,
`services --start --skip-build` cannot reuse the default `${artifact}`. If a
service declares a non-empty `runner.build` and
its `runner.run` references that default artifact, Conven fails before opening a
connection or starting a process. To reuse a build output during
`services --start`, set `runner.artifact` explicitly to a persistent workspace
path such as `${serviceDir}/bin/service`, and make sure the file already exists.
`services --restart` does not reset `current`, so
`services --restart --skip-build` can reuse its existing default artifact.

`services --start --dry-run` only reads the local manifest, source tree, and
static configuration needed to build and validate the plan. It does not create,
reset, or otherwise modify the runtime directory; contact Apollo; establish a
network connection; or execute materialization, prepare, build, or service
commands. A failed start retains its partial `current` directory for
diagnosis after rollback; the next safe fresh start resets it.
`services --stop` never deletes `current`; when all session services have been
stopped, Conven clears the session and releases its connection lease while
retaining artifacts, configs, and logs until that next fresh start.

`services --stop` accepts service names or `--all` for every service in the
current workspace session. If a process leader has exited, or its identity no longer
matches while the saved process group is still alive, a normal stop preserves
the state and refuses to risk killing the wrong process.
`conven services --status` displays the saved PID and PGID. After confirming that
the PGID belongs to the Conven session, use
`conven services --stop --force SERVICE` for one service or
`conven services --stop --all --force` for the full session. `--force` bypasses
identity verification and signals the saved PGID directly; use it only for a
manually verified recovery.

`conven services --stop-all` is the exact shorthand for
`conven services --stop --all`: both use the same service cleanup and connection
release path. They release only the current workspace's connection lease and
terminate the owned ktctl connection only when no active workspace lease
remains; they never terminate a connection still leased by another workspace.
An external ktctl process or network path recorded with both `Owned=false` and
`Managed=false` is not owned by Conven. Both forms remove only the current session
reference and never terminate that external connection.

When a workspace has no session, `conven services --status` lists shared
connection records from the current user's effective Conven state root, including
fingerprint, PID/PGID, and effective lease count. After confirming the target,
`conven services --stop --all --force` examines those records and force-removes only
connections without active workspace leases. Active leases are retained;
ordinary stale leases are reclaimed after a fixed five-minute grace period.
This path recovers connection process groups and records left behind after Conven
exits unexpectedly. The recovery command must still run from a workspace with a
valid discoverable manifest. The effective user state root comes from
`LOOM_STATE_HOME`, `XDG_STATE_HOME`, or the default user state directory.

`services --logs` accepts service names and supports the boolean `--tail` switch
described above. `doctor` accepts `--env`, `--dev`, `--test`, `--kubeconfig`,
`--context`, and `--namespace`.

Use the corresponding command's `--help` output as the authoritative flag
reference.

## Git-style configuration

Conven stores user-wide settings in `~/.loom/config` and workspace settings in
`.loom/config`. Without `--global`, reads and `--list` show the effective merged
configuration, with local values overriding global values; writes and `--unset`
change only the local file. With `--global`, every operation targets only the
user-wide file.

```bash
# Effective local-over-global values; requires a .loom workspace boundary.
conven config --list
conven config ktctl.path
conven config ktctl.path /opt/homebrew/bin/ktctl
conven config ktctl.kubeconfig /secure/dev-kubeconfig
conven config --unset ktctl.path
conven config --unset ktctl.kubeconfig

# User-wide scope; also works outside a workspace.
conven config --global --list
conven config --global ktctl.path '~/bin/ktctl'
conven config --global ktctl.kubeconfig '~/.kube/dev-config'
conven config --global --unset ktctl.path
conven config --global --unset ktctl.kubeconfig
```

If a local key is removed, a global value with the same name becomes effective
again. The files are flat YAML maps and are created with user-only permissions.
The ktctl runtime settings consumed by Conven are `ktctl.path` and
`ktctl.kubeconfig`. `ktctl.path` accepts an absolute path, a path beginning with
`~/`, or a command name resolved through `PATH`. A relative
`ktctl.kubeconfig` is resolved from the workspace; absolute and `~/` paths are
also accepted.

## Workspace boundary and manifest discovery

The only recognized manifest is `<workspace>/.loom/loom.yaml`; a `loom.yaml` at
the workspace root and alternate files inside `.loom` are ignored. Starting at
the current directory, Conven walks upward and stops at the nearest `.loom`
directory. That directory is a hard workspace boundary: if it does not contain
`loom.yaml`, Conven reports an incomplete workspace and does not continue to a
parent workspace. If no `.loom` directory is found, the current directory is
outside a Conven workspace. The user-wide `~/.loom` directory is reserved for
global settings and is never a workspace boundary. `conven init` therefore
refuses the user home directory; initialize a project directory instead.

There is no CLI or environment override for workspace discovery. To run a
command against another workspace, change directory in the invoking shell or
script:

```bash
(cd /path/to/workspace && conven services --status)
```

Each workspace has one canonical manifest. Select environment-specific values
through `--env`, `--dev`, or `--test` and the corresponding `environments`
profile rather than selecting another manifest.
That `.loom/loom.yaml` is also the workspace's centralized self-description: it
holds repository paths, static analysis results, runners, ports, dependencies,
environment connections, and reusable policies. Service declarations produced
by discovery update only this file. `conven init` may also merge `/runtime/` into
the central `.loom/.gitignore`, but it never writes Conven configuration or
runtime copies into child service repositories.

`conven policy` also operates on this one canonical file; Conven never creates or
reads `.loom/policy.yaml`. It requires the nearest real `.loom` boundary but can
open, import-replace, template-replace, or scan-rebuild `loom.yaml` while its
contents are invalid. Import, template install, or scan reset can recreate a
missing manifest inside an existing boundary. All four policy actions reject a
symbolic-link boundary or manifest.

Runtime workspace commands (`services` and `doctor`) require this boundary and
a valid resolved manifest.
Outside a workspace, only `help`, `--help`, `--version`, `init`,
`config --global`, and internal completion generation are operational.
Command-specific `--help` can also be displayed anywhere. `config` without
`--global` uses the nearest `.loom` hard boundary and can be used there while
the manifest is still being prepared.

Conven injects the resolved absolute workspace root into every local service as
`LOOM_WORKSPACE`, replacing any inherited value. This is read-only metadata for
the service process; the Conven CLI never reads it to discover or select a
workspace.

A v1 manifest must satisfy all of the following:

- `version` is `1`.
- `workspace.name` is non-empty.
- At least one service is declared.
- Every service has a `path` and a non-empty `runner.run` argv.
- A service name starts with a letter or digit and contains only letters,
  digits, `.`, `_`, or `-`.
- `prepare`, `build`, and `run` contain no empty argv elements.
- Ports are in range, and dependencies reference services in the same manifest.

Minimal example:

```yaml
version: 1

workspace:
  name: demo

services:
  user-svc:
    path: services/user-svc
    runner:
      workdir: .
      build: [go, build, -o, "${artifact}", ./cmd/server]
      run: ["${artifact}"]
```

See [`examples/application.yaml`](examples/application.yaml) for representative
fields and multilingual runner examples. Commands are argv arrays, not shell
strings, so `&&`, pipes, and redirections are not interpreted implicitly. When
shell behavior is required, declare `sh -c` explicitly in the argv and handle
quoting risks in the manifest.

### Key fields

| Field | Purpose |
| --- | --- |
| `workspace.name` | Stable workspace name |
| `workspace.policy` / `services.<name>.policy` | Workspace default policy and an optional per-service override |
| `policies.<name>.drivers` | Framework, configuration source, discovery, and materializer selection |
| `policies.<name>.config` | Source directory, application/bootstrap, Apollo retry settings, and shared YAML patches |
| `policies.<name>.process` | Environment and argv appended uniformly by the policy |
| `policies.<name>.routing` | Server-port patches by service kind and local/remote dependency YAML routing |
| `environments.<name>.env` | Environment variables shared by all local services in the selected environment |
| `environments.<name>.registry` | Descriptive registry type; v1 does not interpret it automatically |
| `environments.<name>.connection` | `none`, `ktctl`, or `command` connection configuration |
| `environments.<name>.connection.command` | Optional `ktctl` executable fallback, or the required executable for the `command` driver |
| `environments.<name>.connection.sudo` | Optional; start and stop the connection through `sudo` |
| `services.<name>.path` | Service directory, absolute or relative to the workspace |
| `services.<name>.kind` / `discovery` | Service type plus analyzer and statically extracted binding candidates |
| `runner.workdir` | Prepare/build directory, absolute or relative to the service directory |
| `runner.runWorkdir` | Optional run and command-health directory; supports templates and defaults to `runner.workdir` |
| `runner.prepare/build/run` | argv executed in order; `run` is required |
| `runner.artifact` | Optional build artifact path |
| `ports` | Named port-to-number map available to templates |
| `env` / `localEnv` | Common service variables and local-start variables |
| `dependencies` | Uses `binding`/`port` for YAML routing, or injects `localEnv`/`remoteEnv` according to the selection |
| `health` | `process`, `tcp`, `http`, or `command` health check |

### Policies, drivers, and configuration materialization

A policy declares a company or framework convention once. Services inherit
`workspace.policy` unless they select their own `policy`. Driver responsibilities
are currently bounded as follows:

Policy definitions, service selection, and environment declarations remain in
the same manifest. Use `conven policy --edit` for validated manual changes and
`services --registry` to conservatively refresh facts discovered from
repositories. Use `policy --import <yaml-file> [--edit]` to adopt a complete
local candidate without merging or modifying its source. `policy --reset` can
reconstruct only scan facts; it cannot reconstruct a policy.

| Driver | Current role |
| --- | --- |
| `framework`, `discovery` | Classify the policy for planning and diagnostics; they do not themselves start a framework or registry |
| `configSource: repository` | Read YAML from a service-repository directory selected by policy `config.sourceDir` |
| `configSource: apollo` | Read Apollo connection metadata from the bootstrap and fetch application content |
| `materializer: yaml-overlay` | Copy to staging, apply policy/server/service/dependency patches, validate, and publish atomically |

Materialized output always goes to:

```text
<workspace>/.loom/runtime/current/configs/<service>/
```

Source files such as `resources/application.yaml` and bootstrap YAML remain
read-only. The `repository` source starts from the repository application copy;
the `apollo` source replaces application content with the fetched configuration
and can publish a separate runtime bootstrap. Conven applies shared policy patches,
kind-specific server patches, and `services.<name>.config.patches`, then applies
the dependency route selected for this run. Registry refresh preserves manually
populated non-empty manifest fields. Service patches override shared/server
defaults, while the final dependency route enforces the selected local/remote
topology.

The materializer copies only the policy `config.sourceDir` content into
`configs/<service>`; it does not automatically construct a script-style full
`go/ + resources/` runtime tree. A policy can pass `-f ${configDir}` to consume
the materialized application/bootstrap while leaving `runner.runWorkdir`
unset, in which case the process cwd remains the source workdir. Other relative
paths such as `../resources/...` continue to read source resources without
modifying them. A service that requires a fully independent runtime layout must
declare `runner.runWorkdir` and the corresponding prepare/layout rules explicitly.

Keep complete company or project declarations in the project's own repository.
Generate a credential-free candidate there, review it with
`conven policy --import <yaml-file> --edit`, and commit the resulting canonical
`.loom/loom.yaml`. Machine-local kubeconfig paths can be set with
`conven config ktctl.kubeconfig <path>`; credentials should stay in environment or
external credential systems.

### Separate run directory

Use `runner.runWorkdir` when prepare or build steps run in the source tree but
the service must start from generated runtime resources:

```yaml
services:
  api:
    path: services/api
    runner:
      workdir: .
      runWorkdir: "${runDir}/configs/${service}/runtime"
      prepare: [mkdir, -p, "${runDir}/configs/${service}/runtime"]
      build: [go, build, -o, "${artifact}", ./cmd/server]
      run: ["${artifact}"]
    health:
      type: command
      command: [test, -d, .]
```

`prepare` and `build` continue to execute in `runner.workdir`. The service
process and a `command` health check execute in `runner.runWorkdir`. An absolute
run workdir is used as-is; a relative value is resolved from the service
directory, not from `runner.workdir`. If omitted, it defaults to the resolved
`runner.workdir`.

When `prepare` is declared, the run workdir may be absent while the manifest is
planned and `prepare` may create it. Without `prepare`, it must already exist.
Conven checks that it is a directory after prepare/build and immediately before
starting the process. During restart, this check happens for every target before
any old target process is stopped.

Prefer a generated run workdir under `${runDir}`, which resolves to
`<workspace>/.loom/runtime/current`. If it is inside the service
directory, make sure generated files are ignored by source control; otherwise
the source fingerprint may select the service again on the next argument-free
`services --restart`.

Local dependency graphs may contain cycles. Conven first groups each cycle into a
strongly connected component, then starts components in dependency order.
Services within one component are sorted by name, and every process in the
component is started before health checks begin.

`runner.runWorkdir`, runner argv, environment values, health-check addresses and
commands, and connection command/args/kubeconfig/context/namespace/readiness
addresses may use these templates:

```text
${workspace}  ${service}  ${serviceDir}  ${stateDir}
${runDir}     ${artifact} ${env}
${port.NAME}
${services.SERVICE.ports.NAME}
```

Workspace runtime templates and injected environment variables have stable
meanings:

| Template / variable | Resolved path |
| --- | --- |
| `${stateDir}` / `LOOM_STATE_DIR` | `<workspace>/.loom/runtime` |
| `${runDir}` / `LOOM_RUN_DIR` | `<workspace>/.loom/runtime/current` |
| `${artifact}` / `LOOM_ARTIFACT` | `current/artifacts/<service>` by default |
| `LOOM_CONFIG_DIR` | `current/configs/<service>` |

The `ktctl` driver appends `connect` automatically, so `connection.args` must
contain only additional arguments. An enabled connection requires at least one
TCP `readiness` endpoint. If all endpoints are already reachable, Conven reuses
the existing network path instead of starting another connection process.

Connections started by Conven use a current-user global lock, persistent records,
and workspace leases. When multiple workspaces reuse one managed ktctl
connection, releasing one workspace does not interrupt the others. Conven stops
the connection only after the final lease is released. If networking is already
provided by an external process, Conven reuses reachability without taking
ownership. Set `connection.sudo: true` when a custom connection must run as
root. Conven first runs interactive `sudo -v`, starts through `sudo -n`, and tracks
the actual connection descendant rather than only the outer sudo process. If
the sudo timestamp has expired at shutdown, Conven requests authorization again.

## Local and remote routing

Assume this run selects `user-svc` and `order-svc`:

```text
user-svc -> order-svc   uses user-svc.dependencies.order-svc.localEnv
user-svc -> payment-svc uses user-svc.dependencies.payment-svc.remoteEnv
```

Conven supports two explicit routing contracts:

1. With the legacy environment contract, Conven injects dependency `localEnv` or
   `remoteEnv` according to selection. The application must consume those values.
2. With the policy YAML contract, a dependency declares the target service's
   `binding` and named `port`. A selected dependency uses
   `routing.localDependency` (for example, replacing it with
   `127.0.0.1:${dependency.port}`); an unselected dependency uses
   `remoteDependency` (commonly preserving its Consul/Apollo discovery data).

The second contract modifies only
`.loom/runtime/current/configs/<service>`, never YAML inside a service
repository. Use `environments.<name>.env` for environment-wide values, then
`services.<name>.env`, `services.<name>.localEnv`, and dependency environment
values for successive overrides. A selected dependency must have at least one local
routing contract, otherwise Conven rejects the ambiguous plan before startup.

`connection.driver: ktctl` provides only network reachability from the local
machine to the cluster. It does not make cluster services able to call local
processes, and it does not replace service discovery, configuration services,
or application authentication.

## ktctl executable selection

For `connection.driver: ktctl`, Conven selects the executable in this order:

1. `ktctl.path` in the workspace's `.loom/config`.
2. `ktctl.path` in `~/.loom/config`.
3. The current environment's manifest `connection.command`.
4. `ktctl` resolved through `PATH`.

For example, keep a machine-specific binary path out of the shared manifest:

```bash
conven config ktctl.path /absolute/path/to/ktctl
# Or set a default for every workspace:
conven config --global ktctl.path ktctl-custom
```

This setting applies only to the `ktctl` driver. A `command` driver always uses
its manifest `connection.command` and is not affected by `ktctl.path`. Before
launch, Conven resolves a PATH command to its executable path; this keeps
`connection.sudo: true` working even when sudo has a restricted `secure_path`.
A relative manifest command containing a path separator is resolved from the
workspace root before that lookup.

## kubeconfig input and precedence

The final kubeconfig path is resolved in this order:

1. CLI `--kubeconfig FILE`.
2. `LOOM_KUBECONFIG`.
3. `KTCTL_KUBECONFIG`, for compatibility with existing scripts.
4. The environment variable named by the current environment's
   `connection.kubeconfigEnv`.
5. Effective `ktctl.kubeconfig` from local `.loom/config`, then
   `~/.loom/config`.
6. The current environment's `connection.kubeconfig`.
7. `KUBECONFIG`.
8. `$HOME/.kube/config`.

Examples:

```bash
LOOM_KUBECONFIG=/secure/dev-kubeconfig \
  conven doctor --env dev --context dev-cluster --namespace dev

# Requires an environments.test profile in the manifest.
KTCTL_KUBECONFIG=/secure/test-kubeconfig conven doctor --test

conven config ktctl.kubeconfig /secure/dev-kubeconfig

conven services --start --dev \
  --context dev-cluster \
  --namespace dev \
  user-svc order-svc
```

With `connection.sudo: true`, the effective launch shape is
`sudo -n <resolved-ktctl> --kubeconfig <file> ... connect`; Conven performs
interactive `sudo -v` authorization first. `KTCTL_KUBECONFIG` is therefore a
directly supported input, not an environment variable that must be forwarded
manually through `connection.args`.

v1 requires the resolved kubeconfig to be a single file. Conven rejects a
multi-file `KUBECONFIG` list rather than passing it to a connection tool that may
not implement Kubernetes merge semantics. Kubeconfig files may contain
credentials; do not commit them to the manifest or repository. Prefer a CLI or
environment-variable path to a personal credential file.

## Runtime state and logs

Workspace runtime state always lives beside the manifest; it is not selected by
the manifest or user state environment variables:

```text
<workspace>/.loom/runtime/
├── .lock
├── session.json
├── connection.log
└── current/
    ├── artifacts/
    ├── configs/
    └── logs/
```

Pre-reset policy snapshots are separate from runtime state. They are retained in
`<workspace>/.loom/backups/`, are ignored by the workspace `.gitignore`, and are
not removed by start, restart, or stop. Remove them manually after the reset
declarations have been reviewed and committed or backed up elsewhere.

Because the runtime is inside the canonical workspace, separate workspaces with
the same `workspace.name` remain isolated, while access through a symlink still
resolves to the same runtime.

`.lock` and `connection.log` remain outside `current`, so a fresh start can hold
the workspace lock while safely resetting `current` without deleting the stable
connection log. Conven truncates `connection.log` only when it actually starts a
new connection from this workspace. Reusing a managed shared connection retains
the existing log and its recorded owner-workspace path; reusing an external
network path does not create or change the file.

Before resetting `current`, Conven verifies saved PID/PGID and process identity.
An active or untrusted process blocks startup and leaves all runtime files
untouched. Runtime directories use user-only directory permissions; session,
lock, and log files use user-only file permissions. Conven rejects symlinks,
non-directories, and paths outside the canonical workspace runtime before
deleting or recreating `current`.

`doctor`, start dry-run, and status report this fixed runtime path. Runtime
changes under `.loom` are excluded from source fingerprints and do not trigger
restart. Conven does not create historical run directories: restart reuses
`current`, stop retains it for inspection, and the next safe
`services --start` replaces it.

`workspace.stateDir` has been removed from the manifest schema and is rejected
as an unknown field. Conven does not discover, migrate, or delete workspace
runtime data created by older builds in the user state directory; remove such
data manually only after confirming no process from the older build is running.

Current-user shared connection records still live under `.connections` in the
Conven user state root selected, in order, by `LOOM_STATE_HOME`,
`$XDG_STATE_HOME/loom`, or `$HOME/.local/state/loom`. These settings affect only
the small cross-workspace connection registry; they never relocate workspace
artifacts, generated configuration, service logs, locks, or session state.

## Development

Run local checks from the repository root:

```bash
go test ./...
go vet ./...
go build ./cmd/conven
```

For the complete release and tap verification procedure, see
[`RELEASING.md`](RELEASING.md).

## License

This project is licensed under the MIT License. See [`LICENSE`](LICENSE).
Version changes are recorded in [`CHANGELOG.md`](CHANGELOG.md).
