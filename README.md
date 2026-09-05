# Conven

**English** | [简体中文](README-ZH.md)

[![CI](https://github.com/leo1394/homebrew-conven/actions/workflows/ci.yml/badge.svg)](https://github.com/leo1394/homebrew-conven/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

![Conven — Run changed services locally and keep cluster dependencies connected](assets/conven-banner-en.png)

> **A verifiable local microservice orchestrator.**
>
> Run selected services locally, route the rest explicitly, and verify runtime isolation.

Conven is a focused local microservice orchestrator. It runs a selected service
group locally and keeps the rest reachable through configured endpoints or a
development cluster. Generated configuration stays outside service repositories.

- **Start less:** run only the services involved in the current change.
- **Keep the real topology:** mix local services with remote RPC, databases,
  Kafka, configuration services, and other development dependencies.
- **Fail closed:** supported typed services do not start unless Conven can
  verify local registration and listener isolation.
- **Use any language:** prepare, build, and run steps are argv arrays, not
  Go-specific hooks.

## Why Conven

Starting an entire microservice system on a laptop is usually slow and
unnecessary. Starting one service alone is faster, but then configuration,
service discovery, local routing, logs, and process cleanup become a collection
of project-specific scripts.

Conven turns those conventions into one reviewed workspace manifest:

```mermaid
flowchart LR
    M[".conven/conven.yaml"] --> P["plan + safety checks"]
    P --> R[".conven/runtime/current"]
    R --> A["local api"]
    R --> B["local rpc"]
    E["configured local endpoints"] --> A
    E --> B
    A -->|"127.0.0.1"| B
    A -->|"ktctl + remote discovery"| D["development dependencies"]
    B -->|"ktctl + remote discovery"| D
```

The source repositories remain the place you edit code. Runtime YAML copies,
artifacts, logs, and session state live under the workspace's `.conven/runtime`.

## Safety by design

For a classified service with `kinds: [http]`, `kinds: [rpc]`, or multiple
listeners, Conven starts only when a trusted adapter can verify the final
runtime plan:

- remote service registration is disabled, or registration is explicitly not
  applicable to that server kind;
- the service listener uses the declared scope, defaulting to a loopback IP;
- the run argv points to Conven's guarded runtime configuration;
- the connection creates no cluster-to-local inbound route.

If any proof is missing or ambiguous, startup fails closed. An analyzer extracts
source facts, a certifier compiles them with the unique matching policy into a
trusted runtime contract, and core orchestration consumes that contract without
framework-specific branches. After startup, Conven also verifies listener
ownership and registry changes. See the
[typed-service support matrix](docs/typed-service-support.md) for supported
frameworks, config delivery, and registries. Conven can verify the plan and
observable results; it cannot prove that an arbitrary binary honors its flags.

Typed services listen on loopback by default. To let another device on the
same LAN reach one specific service, opt that service into all interfaces:

```yaml
services:
  portal-api-service:
    kinds: [http]
    network:
      listen: all-interfaces
```

Conven then enforces `0.0.0.0` for that service and prints a startup warning.
This exposes every host interface, not only the LAN interface, so the host
firewall still defines actual reachability. Local service routes and health
checks continue to use `127.0.0.1`. Omit `network.listen`, or set it to
`loopback`, for the default local-only behavior. Arbitrary bind addresses are
not accepted.

The equivalent per-service switches are:

```bash
conven services --listen --on portal-api-service
conven services --listen --off portal-api-service
```

They update `.conven/conven.yaml` atomically and do not restart a running
process. The selected scope takes effect on the next `services --start` or
`services --restart`.

Conven's built-in materializer writes generated YAML only to
`.conven/runtime/current/configs/<service>`; it does not overwrite repository
YAML. Fresh start validates saved process identity and runtime paths before
cleaning `current`. Stop and rollback also verify PID/PGID ownership before
signalling a process group. If cleanup cannot be proven complete, Conven keeps
the session and blocks the next fresh start.

> **Local isolation is not data isolation.** Local services still use the
> remote databases, Kafka brokers, unselected RPC clients, and background jobs
> present in their runtime configuration. They may write data or consume
> messages. Conven does not sandbox those effects. Until unified local routing
> for asynchronous workloads is implemented, Conven defaults
> `SERVICE_KAFKA_CONSUMERS_ENABLED` to `true` and does not require a source
> guard. Only an explicit `false` requests Kafka consumer isolation; Conven
> then verifies that the service can honor the switch before startup.

Runner-only services with no `kinds` do not receive the same adapter-backed
isolation guarantee. Project-defined `prepare` and `build` commands also run
with the user's normal authority and may modify their working directory.

## Install

### Homebrew (recommended)

```bash
brew install leo1394/conven/conven
```

After installation, the short name works for upgrades:

```bash
brew update
brew upgrade conven
```

### Bash 

If the installed Homebrew is too old to install the Formula, build and install
the published release with Bash:

```bash
curl -fsSL https://raw.githubusercontent.com/leo1394/homebrew-conven/master/install.sh | bash
```

This fallback requires `curl`, `tar`, and Go 1.23 or later. It verifies the
source archive against the repository's published SHA256 manifest, builds
Conven, and installs it to `~/.local/bin`. Add that directory to `PATH` if
prompted. Run the command again to upgrade. To choose a version or destination:

```bash
curl -fsSL https://raw.githubusercontent.com/leo1394/homebrew-conven/master/install.sh | CONVEN_VERSION=1.0.2 bash
curl -fsSL https://raw.githubusercontent.com/leo1394/homebrew-conven/master/install.sh | CONVEN_INSTALL_DIR=/absolute/bin bash
```

Conven supports macOS and Linux. Homebrew and `install.sh` install Conven only;
they do not install project language runtimes or package managers. `ktctl` is
required only when the selected environment uses that connection driver. A
matching Homebrew bottle does not require Go on the client.

## Quick start

### Start from zero without a cluster

Create a local environment and inspect the available services:

```bash
cd /path/to/workspace

conven init --local
conven services --list
```

Pass service names directly to start local services. Use `--with-dependencies`
to include transitive local service dependencies:

```bash
conven services --start user-svc order-svc
conven services --start order-svc --with-dependencies
```

By default, only explicitly selected services start. Declare the addresses
required by local services as environment endpoints and map them into each
consumer:

```yaml
environments:
  local:
    connection:
      driver: none
    endpoints:
      postgres:
        protocol: tcp
        address: 127.0.0.1:5432

services:
  order-svc:
    path: services/order-svc
    runner:
      run: [go, run, ./cmd/order-svc]
    dependencies:
      postgres:
        env:
          DATABASE_URL: postgres://dev:dev@${dependency.address}/app
```

Conven checks referenced endpoints before starting a service. A sole environment
is selected automatically:

```bash
conven doctor --env local
conven services --start user-svc order-svc
conven status
```

The Chinese
[local-first beginner guide](docs/getting-started-local-zh.md) contains the
complete walkthrough.

### Use an existing Conven workspace

If the project already commits `.conven/conven.yaml`:

```bash
cd /path/to/workspace

conven doctor --test
conven services --start --test portal-api-service partner-service visit-plan-mgr-service
```

Version 1.0 uses Manifest v3. Stop any active session before running
`conven workspace --migrate` for a v1 or v2 workspace. Conven backs up and fully
validates the manifest before replacing it atomically. If registry identity,
runtime, or policy cannot be proven, migration fails without replacing the
original file.

Replace the example names with values from `conven services --list`. In an
interactive terminal you may omit the names and select services in the picker.
After startup, Conven opens the Dashboard by default. Press `q` or `Ctrl-C` to
detach; the services keep running.
![Conven services Dashboard](assets/conven-services-dashboard-snapshot.png)

Stop the workspace session explicitly:

```bash
conven services --stop-all
```

### Onboard a project

Run `init` from the directory containing the service repositories:

```bash
cd /path/to/workspace

conven init
conven workspace --validate
conven services --list
conven workspace --edit

conven doctor --dev
conven services --start --dev --dry-run portal-api-service partner-service
conven services --start --dev portal-api-service partner-service
```

`init` performs the initial registry scan, conservatively identifying Git
repositories that are immediate children of the workspace. It initializes these
files:

| File | Purpose |
| --- | --- |
| `.conven/conven.yaml` | Canonical workspace manifest for environments, services, policies, and runtime behavior. |
| `CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md` | AI-readable specification for implementing the workspace policy generator plugin. |
| `README.md` | Workspace-local quick start for the generated files and Conven workflow. |

Each file is created only when missing; an existing regular file is preserved.
`.conven/conven.yaml` is the sole service inventory. `init` and later
`services --registry` runs perform static analysis only: they do not build or
access the network. They record proven runners, listeners, registration code,
bindings, and health checks, and assign the lowest unused port from `18080` to
new services without renumbering existing assignments. A known framework with
insufficient evidence fails the registry update atomically; an unsupported
repository is listed under skipped repositories with a concrete reason.

Conven supports common HTTP/RPC frameworks for Go, Spring Boot, Python, Node.js,
and Bun, plus passive, Kubernetes DNS, Consul, Nacos, Eureka, and Etcd contracts.
It does not infer a complete business dependency graph, organization policy,
credentials, or cluster connection details. See the
[typed-service support matrix](docs/typed-service-support.md) and
[service runtime configuration contract](docs/service-runtime-config-contract.md)
for exact boundaries and repair examples.

If the project maintains a policy generator, install it and run the sole
workspace plugin or name it explicitly. Complete the workspace-specific
`conven-generator.json` required by the generated AI specification before the
first run; `init` does not guess environments or connection policy:

```bash
conven plugins --install ./generate-workspace-policy.py
conven plugins --run --output
conven workspace --import --edit
```

Conven currently bundles no project-specific plugins. `workspace --import`
validates and replaces the complete manifest; it is not a YAML merge.

## How a start works

1. Resolve the nearest `.conven/conven.yaml` and selected environment.
2. Select services, resolve every dependency edge, and build start groups.
3. Validate local-service, endpoint, remote, and disabled routes plus isolation,
   runtime configuration consumption, local module replacements, and paths.
4. Check the readiness of referenced external endpoints.
5. Reuse or establish the environment connection and materialize runtime config.
6. Run prepare, build, start, and health checks, verify listener ownership and
   registry changes, then save state and logs.

### Configuration materialization order

Step 5 follows two related pipelines. Here, `runtime/current/application.yaml`
is shorthand for a service-scoped runtime copy; the actual file lives at
`.conven/runtime/current/configs/<service>/application.yaml`. The full Apollo
replacement applies only to this guarded runtime copy and never overwrites the
repository file.

The end-to-end path from the repository baseline to remote-dependency
preflight is:

```text
repository resources/application.yaml
  → full-document replacement from Apollo application.yml
  → Conven manifest patches
  → runtime/current/application.yaml
  → Consul preflight
```

When Apollo `application.yml` supplies the runtime baseline, patches and the
safety guard are applied in this order:

```text
Apollo application.yml
  → policy patch
  → server patch
  → services.portal-api-service.config.patches
  → dependency-routing patch
  → workspace.disabledBindings patch for bindings that exist
  → local-isolation guard
  → runtime/current
```

The second pipeline also defines precedence: each later patch operates on the
result of the previous stage. `services.portal-api-service.config.patches` is
a concrete example of a service-scoped manifest patch.
`workspace.disabledBindings` disables matching clients only when they exist in
the fetched configuration; it never creates a missing binding. The
local-isolation guard enforces and verifies final listener and registration
behavior, while
Consul preflight checks the remote dependencies that remain enabled in the
final runtime configuration.

### Service runtime configuration contract

> **Important:** A typed service must accept the runtime-configuration argument
> declared by its adapter and actually load configuration from that path.
> Merely receiving, declaring, or ignoring the argument does not satisfy the
> Conven contract. A missing path, invalid path, or parse failure must terminate
> the service with a non-zero status; the service must not fall back to a
> repository-default configuration.

Conven does not require every language to use one hard-coded flag. Application
source stays tool-agnostic and recognizes its framework-native or project-native
configuration option; the manifest or policy adapts `${configDir}` to argv:

| Language/framework | Recommended runtime option |
| --- | --- |
| Go/go-zero | `-f <runtime-config-directory>` |
| Java/Spring Boot | `--spring.config.location=file:<runtime-config-directory>/` |
| Python | `--config <runtime-config-file>` or `--config-dir <runtime-config-directory>` |
| Node.js | `--config <runtime-config-file>` or `--config-dir <runtime-config-directory>` |
| Dart | `--config <runtime-config-file>` or `--config-dir <runtime-config-directory>` |
| Rust | `--config <runtime-config-file>` or `--config-dir <runtime-config-directory>` |

A correct implementation performs both steps: parse the argument, then load
configuration from that path. Parsing it while continuing to load repository
configuration is also invalid. `CONVEN_CONFIG_DIR` is orchestration metadata for
runner hooks and the Conven process; it is not the recommended application-source
integration API. See the [service runtime configuration contract](docs/service-runtime-config-contract.md)
for complete implementations, invalid examples, and the canary-port behavioral
test.

Trusted adapters inject protected host, port, and registration-disable settings
last. Missing, duplicate, or conflicting settings are rejected before startup.
Application code with custom registration must expose a framework-native or
neutral disable switch and must not depend on Conven-specific variables. See the
[service runtime configuration contract](docs/service-runtime-config-contract.md)
for language-specific contracts and repair examples.

`services --start --dry-run` stops after static planning. It does not contact
Apollo, establish a connection, materialize configuration, build code, start a
process, or modify the runtime directory.

For a declared dependency, selection determines the route:

```text
selected dependency      -> local address from the manifest policy
unselected dependency    -> remote discovery/configuration is preserved
```

The plan's **Declared remote dependencies** list covers manifest declarations,
not every endpoint hidden in application configuration. For compatible
go-zero/Consul YAML, Conven separately detects active external Consul clients
and checks for a passing instance before service startup.

## The manifest

Every workspace has one canonical manifest:

```text
<workspace>/.conven/conven.yaml
```

It has four main parts:

| Section | Describes |
| --- | --- |
| `workspace` | Project name and default policy |
| `services` | Repository paths, runners, ports, listener scopes, health checks, and dependencies |
| `policies` | Framework/config drivers, runtime overlays, routing, and isolation |
| `environments` | Environment variables and optional cluster connection |

A minimal runner-only workspace looks like this:

```yaml
version: 3

workspace:
  name: demo

environments:
  dev:
    connection:
      driver: none

services:
  user-svc:
    path: services/user-svc
    runner:
      run: [go, run, ./cmd/user-svc]
    ports:
      http: 18080
    healthChecks:
      - type: process
```

This intentionally omits `kinds`, so it is a generic runner-only example. A
typed HTTP/RPC service must reference a policy with a complete, verifiable
isolation contract. See the [example manifest](examples/application.yaml) for
multiple services, dependency environments, health checks, and a `ktctl`
connection.

Manifest v3 uses `kinds`, `healthChecks`, named registries, explicit discovery
identity, `providerAliases`, and `consumerBindings`. Every kind needs a matching
port, policy server route, and health check; one process may expose multiple
listeners.

Commands are argv arrays. Pipes, redirects, and `&&` are not interpreted unless
you explicitly use a shell such as `[sh, -c, "..."]`.

## Daily commands

| Task | Command |
| --- | --- |
| Inspect the complete workspace state | `conven status` |
| Edit the workspace manifest | `conven workspace --edit` |
| Validate the workspace manifest | `conven workspace --validate` |
| Migrate an older manifest | `conven workspace --migrate` |
| List manifest services | `conven services --list` |
| Refresh scanned repositories | `conven services --registry` |
| Allow LAN access for selected services | `conven services --listen --on SERVICE...` |
| Restore loopback-only access | `conven services --listen --off SERVICE...` |
| Validate one environment | `conven doctor --test` |
| Preview a start | `conven services --start --test --dry-run SERVICE...` |
| Start a local group | `conven services --start --test SERVICE...` |
| Restart changed/exited services | `conven services --restart` |
| Inspect the current session | `conven services --status` |
| Show a log snapshot | `conven services --logs SERVICE...` |
| Open the Dashboard | `conven services --dashboard SERVICE...` |
| Follow plain logs | `conven services --logs --tail SERVICE...` |
| Stop selected services | `conven services --stop SERVICE...` |
| Stop the workspace session | `conven services --stop-all` |
| Remove saved artifacts and logs | `conven services --cleanup` |

Use `--dev`, `--test`, or `--env NAME` to select a declared environment. Add
`--namespace NAME`, `--context NAME`, or `--kubeconfig FILE` when a start needs
a machine-specific Kubernetes override.

Fresh `--start` safely rebuilds `runtime/current`. `--restart` reuses it and
restarts only changed or exited services; unchanged services and a shared
connection stay running. Stop preserves the current logs and generated files
for inspection until the next safe fresh start.

`--start` and `--restart` also watch running services for source changes. A
successful build passes preflight before controlled replacement; a failed build
leaves the last-known-good process running. `--stop-all` stops the watcher.

After `--stop-all`, `--cleanup` removes `runtime/current/artifacts` and
`runtime/current/logs`. It refuses while a session is saved and preserves
runtime configs plus the shared connection log.

## Logs

Conven offers two deliberately different viewers:

| Mode | Best for | Behavior |
| --- | --- | --- |
| Dashboard | Live overview | Fixed workspace banner, wrapped long lines, app-owned scrolling and `/` search, up to 10,000 retained logical lines |
| Plain | Terminal-native search/export | Normal scrollback, `Command+F`, pipes and redirects, up to 10,000 replayed lines before following |

The Dashboard keeps workspace context, local services, disabled bindings, start
time, and live logs in one view.

```bash
# Full-screen viewer; the alias below is equivalent.
conven services --dashboard
conven services --logs --dashboard

# Plain continuous stream.
conven services --logs --tail
```

Interactive starts and restarts open the Dashboard by default; `--tail` selects
Plain mode. The Dashboard supports wrapped logs, scrolling, and `/` search.
Press `g` or `G` to jump to the newest log and continue following; Home jumps
to the oldest log. Press `q` or `Ctrl-C` to detach without stopping services.

## Configuration and plugins

Machine-specific ktctl settings belong outside the shared manifest:

```bash
conven config ktctl.path /opt/homebrew/bin/ktctl
conven config ktctl.kubeconfig /secure/dev-kubeconfig

# Apply a default for every workspace.
conven config --global ktctl.path ktctl
```

Workspace values live in `.conven/config`; global values live in
`~/.conven/config`. Local values override global values. Keep kubeconfig files
and credentials out of source control.

Manage local Python plugins with:

```bash
conven plugins --install ./plugin.py
conven plugins --list
conven plugins --run --output
conven workspace --import
conven plugins --remove plugin

# Use the user-global plugin scope explicitly.
conven plugins --install --global ./plugin.py
conven plugins --list --global
conven plugins --global --run plugin --output
conven plugins --remove --global plugin
```

Workspace plugins live in `.conven/plugins`; global plugins live in
`~/.conven/plugins`. The two scopes may contain the same name. An explicit run
name prefers the workspace copy and warns before falling back to global. When
the workspace has exactly one plugin, the name may be omitted; multiple
workspace plugins open the single selector. If none exists locally, the same
selector shows global candidates, including a sole candidate. An explicitly
global run requires its name. `--output`
without a filename is passed to the plugin, whose generator convention writes
`application.yaml`; `workspace --import` without a filename opens the single
selector for `.yaml` and `.yml` files in the workspace root. Plugins run with
the canonical workspace as their working directory.
Treat them as trusted local code and review generated policy candidates before
import. The grouped default list requires a workspace; outside one, use
`conven plugins --list --global`.

## Runtime layout

```text
<workspace>/.conven/runtime/
├── .lock
├── session.json
├── connection.log
└── current/
    ├── artifacts/
    ├── configs/
    └── logs/
```

Workspace runtime directories and files are user-private. Conven rejects
symlinked or out-of-bound cleanup targets. The only shared runtime state is
connection lease metadata under `~/.conven/state/connections`; business
artifacts, runtime configuration, and service logs never leave the workspace.

## Scope

- Service runners are language-agnostic; automatic repository analysis covers
  the Go, Java, Python, Node.js, and Bun frameworks in the support matrix.
  Unknown repositories can still be declared explicitly as runner-only.
- Configuration can come from a repository, Apollo, or environment variables
  and use YAML, properties, or environment adapters.
- `ktctl connect` provides local-to-cluster reachability. Conven does not use
  `ktctl exchange`, create a reverse route, provide a service mesh, or implement
  preview environments.
- Health checks and listener/registry observers prove only the current local
  start; Conven is not a monitoring system.
- External dependency checks cover recognized bindings only. They are not a
  complete database, Kafka, background-job, or dependency readiness check.

## Help and development

```bash
conven --help
conven help services
man conven
```

The installed manual is the authoritative reference for that Conven version.
The source manual is available at [`docs/conven.1`](docs/conven.1). Release
steps are documented in [`RELEASING.md`](RELEASING.md), and version changes are
in [`CHANGELOG.md`](CHANGELOG.md).

Run repository checks from the project root:

```bash
bash -n install.sh
go test ./...
go vet ./...
go build ./cmd/conven
```

Conven is available under the [MIT License](LICENSE).
