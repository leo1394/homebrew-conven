# Releasing loom

**English** | [简体中文](RELEASING-ZH.md)

This document adapts the release discipline used by `homebrew-gits` to Loom's
Go source build and Homebrew Formula.

## Release model

- Release source from `master` with an immutable annotated tag named
  `vX.Y.Z`.
- The stable Formula downloads the tagged source archive, verifies its SHA-256,
  builds `./cmd/loom` with Go, and generates Bash, Zsh, and Fish completions.
- Keep the `head` stanza so development builds remain available through
  `brew install --formula --HEAD leo1394/loom/loom`.
- Homebrew infers the version from the tag in the stable URL. Do not add a
  redundant `version` stanza.
- Never move, delete, or recreate a published tag. Fix a bad release with a new
  patch version.

The current Formula is HEAD-only. The first stable release adds `url` and
`sha256` after its tag exists. Later releases replace those two values with the
new tag archive.

Because the Formula lives in the same repository as the Go source, its final
SHA cannot be written into the source commit being hashed. Loom therefore uses
two commits:

1. A source release commit, referenced by the immutable tag.
2. A Formula commit that points to that tag and contains its published SHA-256.

## Prerequisites

- Push access to `git@github.com:leo1394/homebrew-loom.git`.
- Go 1.23 or later, Homebrew, Ruby, Git, ripgrep, `curl`, and `shasum`.
- A clean local `master` synchronized with `origin/master`.
- A public GitHub repository and green CI on the current branch.

### One-time repository bootstrap

Complete this section before the normal preflight when `origin` does not exist.
Create the public repository and configure the remote:

```bash
git remote add origin git@github.com:leo1394/homebrew-loom.git
git remote -v
git status --short
```

Review every untracked file before staging it, commit the project baseline, and
push it:

```bash
git add --all
git diff --cached
git commit -m "Initialize loom"
git push -u origin master
```

Do not stage local credentials, kubeconfig files, generated state, or service
logs. If the baseline commit already contains the exact first-release source,
it may be tagged directly after all checks pass; an empty release commit is not
required.

### Release variables

Choose a semantic version before preflight and reuse these release-specific
variables throughout the procedure. The value below is an example:

```bash
LOOM_RELEASE_VERSION=0.1.1
LOOM_RELEASE_TAG="v$LOOM_RELEASE_VERSION"
LOOM_RELEASE_BRANCH="release/$LOOM_RELEASE_TAG"
LOOM_ARCHIVE_URL="https://github.com/leo1394/homebrew-loom/archive/refs/tags/$LOOM_RELEASE_TAG.tar.gz"
LOOM_ARCHIVE_PATH="/tmp/homebrew-loom-$LOOM_RELEASE_VERSION.tar.gz"
LOOM_FORMULA="leo1394/loom/loom"
```

### Preflight for every release

After the one-time bootstrap, synchronize `master`:

```bash
git switch master
git pull --ff-only origin master
git status --short
```

The final command must print nothing. Fetch tags and confirm that the selected
tag does not exist locally or remotely:

```bash
git fetch origin --tags
git tag --list "$LOOM_RELEASE_TAG"
git ls-remote --tags origin "refs/tags/$LOOM_RELEASE_TAG"
```

Both tag lookup commands must print nothing.

Confirm that the release branch does not already exist, then create it from the
synchronized `master`:

```bash
git branch --list "$LOOM_RELEASE_BRANCH"
git ls-remote --heads origin "refs/heads/$LOOM_RELEASE_BRANCH"
git switch -c "$LOOM_RELEASE_BRANCH"
```

The first two commands must print nothing.

## 1. Prepare the version

Update these locations:

| Location | Required change |
| --- | --- |
| `cmd/loom/main.go` | Set the `version` variable |
| `VERSION.txt` | Write only `X.Y.Z` and a trailing newline |
| `CHANGELOG.md` | Add the release date and user-visible changes |
| `Formula/loom.rb` test | Expect `loom X.Y.Z` for the HEAD build |
| `README.md` / `README-ZH.md` | Update only when installation or behavior changed |
| `RELEASING.md` / `RELEASING-ZH.md` | Keep both languages aligned when the release workflow changes |

Do not add the new stable archive URL or SHA yet. They can be known only after
the tag is published.

Check every version occurrence:

```bash
rg -n "$LOOM_RELEASE_VERSION|version =|assert_equal \"loom " \
  cmd/loom/main.go VERSION.txt CHANGELOG.md Formula/loom.rb README.md README-ZH.md
```

## 2. Run local checks

Run all checks from the repository root:

```bash
go mod tidy -diff
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build -o /tmp/loom-release ./cmd/loom
/tmp/loom-release --version
test -f examples/application.yaml
test ! -e examples/loom.yaml

ruby -c Formula/loom.rb
brew style Formula/loom.rb

git diff --check
git diff
git status --short
```

The built binary must print the intended version, for example:

```text
loom 0.1.1
```

Review the diff rather than relying only on automated checks. In particular,
confirm that no credentials, runtime state, logs, or unrelated files are
included.

## 3. Commit and publish the source tag

Stage the required release files and any documentation changed for this
release:

```bash
git add cmd/loom/main.go VERSION.txt CHANGELOG.md Formula/loom.rb
git add README.md README-ZH.md RELEASING.md RELEASING-ZH.md
git diff --cached
git commit -m "Release loom $LOOM_RELEASE_VERSION"
```

Omit unchanged documentation files from `git add`. For the first release, skip
the commit when the already-reviewed baseline is the exact release source.

Record the exact source SHA, push the release branch, and wait for that branch
workflow to pass:

```bash
LOOM_SOURCE_COMMIT=$(git rev-parse HEAD)
git push -u origin "$LOOM_RELEASE_BRANCH"
git fetch origin "$LOOM_RELEASE_BRANCH"
test "$LOOM_SOURCE_COMMIT" = "$(git rev-parse "origin/$LOOM_RELEASE_BRANCH")"
```

Do not create the immutable tag until CI has passed for
`$LOOM_SOURCE_COMMIT`.

Create an annotated tag on that exact commit and inspect it:

```bash
git tag -a "$LOOM_RELEASE_TAG" "$LOOM_SOURCE_COMMIT" -m "loom $LOOM_RELEASE_VERSION"
git show --stat "$LOOM_RELEASE_TAG"
```

Push the tag first so GitHub can materialize its archive:

```bash
git push origin "$LOOM_RELEASE_TAG"
```

Do not move this tag after it is pushed. Wait for the tag workflow to pass
before publishing the Formula. A transient CI job may be rerun, but a source
correction requires a new patch release rather than recreating the tag.

## 4. Publish the stable Formula

Download the exact public tag archive and calculate its checksum:

```bash
curl -fL --retry 5 --retry-all-errors --retry-delay 2 \
  "$LOOM_ARCHIVE_URL" -o "$LOOM_ARCHIVE_PATH"
LOOM_ARCHIVE_SHA=$(LC_ALL=C shasum -a 256 "$LOOM_ARCHIVE_PATH" | awk '{print $1}')
printf '%s\n' "$LOOM_ARCHIVE_SHA"
```

Add or update these stanzas in `Formula/loom.rb`. For example, release `0.1.1`
uses:

```ruby
url "https://github.com/leo1394/homebrew-loom/archive/refs/tags/v0.1.1.tar.gz"
sha256 "<64-character SHA-256>"
head "https://github.com/leo1394/homebrew-loom.git", branch: "master"
```

Keep the existing Go build and completion generation. Do not copy the
single-file installation used by `homebrew-gits`, and do not make optional
tools such as `ktctl` or `sudo` hard Formula dependencies.

Verify the Formula checksum against the downloaded archive:

```bash
FORMULA_SHA=$(ruby -ne 'puts $1 if $_ =~ /^\s*sha256 "([0-9a-f]{64})"/' Formula/loom.rb)
test "$FORMULA_SHA" = "$LOOM_ARCHIVE_SHA"
ruby -c Formula/loom.rb
brew style Formula/loom.rb
git diff --check
git diff -- Formula/loom.rb
```

Commit the Formula separately and push the release branch:

```bash
git add Formula/loom.rb
git commit -m "Update loom formula for $LOOM_RELEASE_VERSION"
LOOM_FORMULA_COMMIT=$(git rev-parse HEAD)
git push origin "$LOOM_RELEASE_BRANCH"
```

Wait for the release-branch workflow to pass for `$LOOM_FORMULA_COMMIT`.
Current CI verifies the HEAD Formula path but does not install the stable URL,
so the following stable Formula gate is mandatory on a disposable Homebrew
test machine:

```bash
LOOM_TEST_TAP="loom-release/loom"
LOOM_TEST_FORMULA="$LOOM_TEST_TAP/loom"
brew tap-new --no-git "$LOOM_TEST_TAP"
LOOM_TEST_TAP_DIR="$(brew --repository "$LOOM_TEST_TAP")"
cp Formula/loom.rb "$LOOM_TEST_TAP_DIR/Formula/loom.rb"

brew audit --formula --strict "$LOOM_TEST_FORMULA"
brew install --formula --build-from-source "$LOOM_TEST_FORMULA"
brew test "$LOOM_TEST_FORMULA"
"$(brew --prefix "$LOOM_TEST_FORMULA")/bin/loom" --version
```

The version must equal `loom $LOOM_RELEASE_VERSION`. After the gate passes,
clean up the temporary test installation and tap:

```bash
brew uninstall --formula "$LOOM_TEST_FORMULA"
brew untap "$LOOM_TEST_TAP"
```

Open a pull request from `$LOOM_RELEASE_BRANCH` to `master`, require its checks
to pass, and merge with a merge commit. A direct fast-forward is also safe when
repository policy permits it. Do not squash or rebase: both the tagged source
commit and Formula commit must remain ancestors of `master`.

After merging, synchronize locally and verify the ancestry:

```bash
git switch master
git pull --ff-only origin master
git merge-base --is-ancestor "$LOOM_SOURCE_COMMIT" origin/master
git merge-base --is-ancestor "$LOOM_FORMULA_COMMIT" origin/master
git status --short
```

## 5. Verify the published artifact

Download the archive again after the branch push and compare it with the
Formula:

```bash
curl -fL --retry 5 --retry-all-errors --retry-delay 2 \
  "$LOOM_ARCHIVE_URL" -o "$LOOM_ARCHIVE_PATH"
LOOM_PUBLISHED_SHA=$(LC_ALL=C shasum -a 256 "$LOOM_ARCHIVE_PATH" | awk '{print $1}')
FORMULA_SHA=$(ruby -ne 'puts $1 if $_ =~ /^\s*sha256 "([0-9a-f]{64})"/' Formula/loom.rb)
test "$LOOM_PUBLISHED_SHA" = "$FORMULA_SHA"
```

Optionally build directly from the extracted archive to isolate source-package
problems from Homebrew:

```bash
LOOM_ARCHIVE_DIR=$(mktemp -d /tmp/homebrew-loom-release.XXXXXX)
(
  tar -xzf "$LOOM_ARCHIVE_PATH" -C "$LOOM_ARCHIVE_DIR" --strip-components=1
  cd "$LOOM_ARCHIVE_DIR"
  go test -count=1 ./...
  go build -o /tmp/loom-published ./cmd/loom
  /tmp/loom-published --version
)
```

## 6. Verify the Homebrew tap

Refresh the tap and audit the published Formula:

```bash
brew tap leo1394/loom
brew update
brew audit --formula --strict --online "$LOOM_FORMULA"
```

Homebrew core contains an unrelated cask named `loom`. Always use
`$LOOM_FORMULA` and `--formula` where the command supports it.

For a machine without Loom installed:

```bash
brew install --formula --build-from-source "$LOOM_FORMULA"
```

For an existing stable or HEAD installation, switch explicitly rather than
using `reinstall`, which preserves the original install options:

```bash
brew uninstall --formula "$LOOM_FORMULA"
brew install --formula --build-from-source "$LOOM_FORMULA"
```

Then verify the Formula and binary:

```bash
brew test "$LOOM_FORMULA"
"$(brew --prefix "$LOOM_FORMULA")/bin/loom" --version
brew info --formula "$LOOM_FORMULA"
brew deps --formula --include-build "$LOOM_FORMULA"
```

Verify all generated completions:

```bash
LOOM_BASH_COMPLETION="$(brew --prefix)/etc/bash_completion.d/loom"
LOOM_ZSH_COMPLETION="$(brew --prefix)/share/zsh/site-functions/_loom"
LOOM_FISH_COMPLETION="$(brew --prefix)/share/fish/vendor_completions.d/loom.fish"
test -e "$LOOM_BASH_COMPLETION"
test -e "$LOOM_ZSH_COMPLETION"
test -e "$LOOM_FISH_COMPLETION"
for completion in "$LOOM_BASH_COMPLETION" "$LOOM_ZSH_COMPLETION" "$LOOM_FISH_COMPLETION"; do
  grep -Fq -- "services" "$completion"
done
for action in list registry status logs start restart stop stop-all; do
  for completion in "$LOOM_BASH_COMPLETION" "$LOOM_ZSH_COMPLETION"; do
    grep -Fq -- "--$action" "$completion"
  done
  grep -Fq -- "-l $action" "$LOOM_FISH_COMPLETION"
done
for action in edit import reset; do
  for completion in "$LOOM_BASH_COMPLETION" "$LOOM_ZSH_COMPLETION"; do
    grep -Fq -- "--$action" "$completion"
  done
  grep -Fq -- "-l $action" "$LOOM_FISH_COMPLETION"
done
for option in prune tail; do
  for completion in "$LOOM_BASH_COMPLETION" "$LOOM_ZSH_COMPLETION"; do
    grep -Fq -- "--$option" "$completion"
  done
  grep -Fq -- "-l $option" "$LOOM_FISH_COMPLETION"
done
! grep -Eq 'compgen -W "[^"]* (discover|start|restart|status|stop|logs|list)( |")' "$LOOM_BASH_COMPLETION"
! grep -Eq "^[[:space:]]*'(discover|start|restart|status|stop|logs|list):" "$LOOM_ZSH_COMPLETION"
! grep -Eq ' -a (discover|start|restart|status|stop|logs|list) -d ' "$LOOM_FISH_COMPLETION"
! grep -Fq -- "--workspace" "$LOOM_BASH_COMPLETION" "$LOOM_ZSH_COMPLETION"
! grep -Fq -- "--config" "$LOOM_BASH_COMPLETION" "$LOOM_ZSH_COMPLETION"
! grep -Fq -- "-l workspace" "$LOOM_FISH_COMPLETION"
! grep -Fq -- "-l config" "$LOOM_FISH_COMPLETION"
```

## 7. Run functional acceptance

Use a disposable development workspace with no strong-match child repository
for the process checks below. In that case, `loom init` must create the fallback
manifest at `.loom/loom.yaml` from the bundled `examples/application.yaml`
template, and a second invocation must leave an existing manifest unchanged.
Edit that generated manifest and provide the referenced service repositories
before starting services. Make at least one selected acceptance service emit a
deterministic, non-ANSI startup log line so the redirected tail assertion has a
known fixture. Test repository discovery separately with generic disposable Git
repositories; do not encode a company-specific service stack as an automated
test. Do not run connection or process-recovery tests against production
infrastructure.

Before running the commands below, prepare
`.acceptance/import-candidate.yaml` as a complete valid Loom v1 manifest for the
same disposable service repositories. Make it differ from the `init` result in
at least `workspace.name`; it must contain no credentials. This exercises a real
replacement and backup rather than the byte-identical no-op path.

At minimum, verify:

```bash
LOOM_BIN="$(brew --prefix "$LOOM_FORMULA")/bin/loom"
LOOM_TEST_KUBECONFIG=/absolute/path/to/disposable/kubeconfig
test -f "$LOOM_TEST_KUBECONFIG"
"$LOOM_BIN" init
test -f .loom/loom.yaml
LOOM_MANIFEST_SHA=$(shasum -a 256 .loom/loom.yaml | awk '{print $1}')
"$LOOM_BIN" init
test "$LOOM_MANIFEST_SHA" = "$(shasum -a 256 .loom/loom.yaml | awk '{print $1}')"
mkdir -p .acceptance/descendant
LOOM_IMPORT_SOURCE=.acceptance/import-candidate.yaml
LOOM_IMPORT_SOURCE_SHA=$(shasum -a 256 "$LOOM_IMPORT_SOURCE" | awk '{print $1}')
(cd .acceptance/descendant && "$LOOM_BIN" policy --import ../import-candidate.yaml)
test "$LOOM_IMPORT_SOURCE_SHA" = "$(shasum -a 256 "$LOOM_IMPORT_SOURCE" | awk '{print $1}')"
cmp "$LOOM_IMPORT_SOURCE" .loom/loom.yaml
test -n "$(find .loom/backups -type f -name 'loom.yaml-before-import-*.bak' -print -quit)"
"$LOOM_BIN" config ktctl.path ktctl
test "$("$LOOM_BIN" config ktctl.path)" = "ktctl"
"$LOOM_BIN" config ktctl.kubeconfig "$LOOM_TEST_KUBECONFIG"
test "$("$LOOM_BIN" config ktctl.kubeconfig)" = "$LOOM_TEST_KUBECONFIG"
"$LOOM_BIN" config --list
"$LOOM_BIN" doctor
"$LOOM_BIN" services --start --dry-run user-svc order-svc
"$LOOM_BIN" services --list
(cd .acceptance/descendant && "$LOOM_BIN" services --list)
(cd .acceptance/descendant && "$LOOM_BIN" services --registry)
LOOM_WORKSPACE=/path/that/is/not/a/workspace "$LOOM_BIN" services --list
for command in services doctor; do
  for removed_flag in --workspace --config; do
    if "$LOOM_BIN" "$command" "$removed_flag" /tmp >/dev/null 2>&1; then
      false
    else
      LOOM_USAGE_STATUS=$?
      test "$LOOM_USAGE_STATUS" -eq 2
    fi
  done
done
LOOM_HELP_OUTPUT=$(mktemp /tmp/loom-help.XXXXXX)
for command in services doctor; do
  "$LOOM_BIN" "$command" --help >"$LOOM_HELP_OUTPUT"
  ! grep -Fq -- "--workspace" "$LOOM_HELP_OUTPUT"
  ! grep -Fq -- "--config" "$LOOM_HELP_OUTPUT"
done
for removed_command in discover start restart status stop logs list; do
  if "$LOOM_BIN" "$removed_command" --help >/dev/null 2>&1; then
    false
  else
    LOOM_USAGE_STATUS=$?
    test "$LOOM_USAGE_STATUS" -eq 2
  fi
done
if "$LOOM_BIN" services --tail --logs user-svc >/dev/null 2>&1; then
  false
else
  LOOM_USAGE_STATUS=$?
  test "$LOOM_USAGE_STATUS" -eq 2
fi
rm -f "$LOOM_HELP_OUTPUT"
"$LOOM_BIN" services --start --dry-run --dev user-svc order-svc
"$LOOM_BIN" services --start --dry-run user-svc order-svc
"$LOOM_BIN" services --start user-svc order-svc
"$LOOM_BIN" services --status
"$LOOM_BIN" services --logs user-svc order-svc
LOOM_TAIL_OUTPUT=$(mktemp /tmp/loom-tail.XXXXXX)
"$LOOM_BIN" services --logs --tail user-svc order-svc >"$LOOM_TAIL_OUTPUT" &
LOOM_TAIL_PID=$!
sleep 1
kill -INT "$LOOM_TAIL_PID"
wait "$LOOM_TAIL_PID"
grep -Eq '^\[(user-svc|order-svc)\] ' "$LOOM_TAIL_OUTPUT"
! LC_ALL=C grep -Fq "$(printf '\033[?1049h')" "$LOOM_TAIL_OUTPUT"
! LC_ALL=C grep -Fq "$(printf '\033[2J')" "$LOOM_TAIL_OUTPUT"
rm -f "$LOOM_TAIL_OUTPUT"
# Edit a tracked source file in one running service before this automatic check.
"$LOOM_BIN" services --restart
"$LOOM_BIN" services --restart user-svc
"$LOOM_BIN" services --stop-all
```

The import check must confirm that the relative path was resolved from
`.acceptance/descendant`, the source hash stayed unchanged, the canonical
manifest exactly matches the complete candidate rather than a field merge, and
the previous target was backed up. Also test an invalid candidate separately:
without `--edit` it must leave both files unchanged; with a deterministic
waiting editor, `--import ... --edit` must edit only a private draft and may
publish a repaired valid result while leaving the source unchanged. Passing
schema validation is not the runtime gate; the subsequent doctor and start
dry-run above must still pass before starting processes.

In a separate disposable workspace, create generic immediate-child Git
repositories to exercise discovery. A strong-match fixture must contain
`go/go.mod`, a `go/main.go` with `package main`, and a module-path basename equal
to the repository directory name. Confirm all of the following without using
real business repositories:

- First `init` replaces the example services with every strong match and defines
  `path` as the repository name, `runner.workdir` as `go`, `runner.build` as the
  argv `[go, build, -o, "${artifact}", .]`, and `runner.run` as the argv
  `["${artifact}"]`. With no strong matches, it uses the embedded example
  instead. Repeated `init` never changes either manifest. A concurrent creator
  must win without being replaced by `init`; publication is atomic no-replace.
- After adding a manual port, environment value, dependency, and YAML comment to
  one discovered service, add another strong-match child repository and run
  `loom services --registry`. The new path is added while the existing service
  block, including the comment, remains unchanged. Running `services --registry`
  from a workspace descendant scans the same workspace root.
- Move a previously discovered repository out of the workspace. Plain
  `services --registry` retains its service entry;
  `services --registry --prune` removes it from the direct-child discovery
  scope. In a separate dependency fixture, pruning a referenced service must
  fail validation and leave the manifest byte-for-byte unchanged.
- Repositories that fail any strong-detector condition are skipped. Discovery
  does not invent ports, environment variables, dependency routing, Apollo
  behavior, or connection configuration for new services.
- A sibling repository with an incomplete `go/main.go` package clause or no
  module directive is reported as skipped while valid repositories are still
  discovered. These ordinary WIP states must not abort the scan.
- Discovery must derive its typed merge and YAML-node edit from the same source
  bytes, strictly decode and validate the final encoded YAML, and require the
  decoded typed manifest to equal the validated candidate. A YAML `<<` merge-key
  fixture that would semantically restore a pruned service must be rejected
  without changing the manifest.
- Immediately before rename, discovery must make its documented best-effort
  same-file and source-bytes check. A conflict detected there leaves the current
  manifest unchanged and asks the user to retry. Acceptance must not describe
  this as a linearizable compare-and-swap against arbitrary external writers;
  the narrow check/rename interval remains a best-effort limitation. A
  symbolic-link `.loom/loom.yaml` must also be rejected rather than replaced.

The redirected `services --logs --tail` invocation above is the non-TTY
acceptance path. It must remain a continuous `[service]`-prefixed plain-text
stream and must not emit dashboard control sequences. ANSI bytes written by a
service may remain in this plain stream; sanitization applies to full-screen
dashboard rendering.

Also exercise the dashboard from a real interactive terminal rather than a
redirect or pipeline:

```bash
"$LOOM_BIN" services --start --tail user-svc order-svc
# Resize the window, then press q. Both services must remain running.
"$LOOM_BIN" services --status
"$LOOM_BIN" services --restart --tail user-svc
# Press Ctrl-C. The restarted service and unchanged service must remain running.
"$LOOM_BIN" services --status
"$LOOM_BIN" services --logs --tail user-svc order-svc
# Press q, then clean up the still-running services.
"$LOOM_BIN" services --stop --all
```

For every TTY entry point, confirm that `--tail` behaves as a boolean switch,
not a line-count option. The full-screen dashboard must keep a fixed banner for
the workspace/environment, selected running services, their manifest-declared
port snapshots, and the current LAN IPv4 address while the remaining viewport
shows the latest aggregated logs and continues scrolling. Resizing the terminal
must redraw the layout, and ANSI or other terminal control sequences written by
a service must be sanitized. Both `q` and `Ctrl-C` must only detach, restore the
terminal, and leave all services running, as verified by the following
`services --status`.
The displayed ports are configuration snapshots rather than live listening-
socket probes; the LAN address and a displayed port do not by themselves prove
that an endpoint is bound or reachable.

Confirm the following behavior:

- `services` and `doctor` resolve only the nearest `.loom/loom.yaml` by walking
  upward from the current directory. A root-level `loom.yaml` and alternate
  files inside `.loom` are ignored. A nested `.loom` without `loom.yaml` is an
  incomplete hard boundary
  and must fail instead of falling back to a valid parent workspace. Repository
  discovery always scans immediate children of the resolved workspace root,
  never children of the invocation directory.
- The `services` action must be its first argument, and exactly one of `--list`,
  `--registry`, `--status`, `--logs`, `--start`, `--restart`, `--stop`, or
  `--stop-all` is required. The former top-level service commands return usage
  status 2 and are absent from help and generated top-level completion
  candidates.
- The removed workspace and manifest flags return usage status 2 for every
  workspace command and are absent from command help and all generated
  completions. Setting `LOOM_WORKSPACE` cannot change CLI discovery: a command
  inside a workspace still uses that workspace, and a command outside one still
  fails.
- Outside a workspace, `init`, `config --global`, help, version, command-specific
  help, and internal completion generation remain available. Workspace-local
  `config` and service commands fail with a clear initialization error. Local
  `config` works at a `.loom` boundary while `loom.yaml` is still being prepared.
  The user-wide `~/.loom` directory is always reserved for global settings: it
  must not make the home directory or its descendants a workspace, even if a
  manifest was created there manually. `loom init` must reject the home directory.
- Every launched local service receives the resolved absolute workspace root in
  `LOOM_WORKSPACE`, overriding an inherited value. Treat it as read-only service
  metadata; it is not an input to CLI discovery.
- Local settings are stored in `.loom/config`, global settings in
  `~/.loom/config`, and local `ktctl.path` and `ktctl.kubeconfig` values override
  global values. Remove both temporary local values with `loom config --unset`
  after the test.
- On `services --start` and `doctor`, `--dev` is equivalent to `--env dev` and
  `--test` is equivalent to `--env test`. Add a disposable `test` profile before
  checking `--test`. A shortcut plus the same `--env` value succeeds;
  `--dev --test` or a shortcut plus a different `--env` value fails before
  workspace startup.
- A workspace has exactly one canonical manifest; environment-specific behavior
  comes from `--env`, `--dev`, or `--test` and the matching `environments`
  profile.
- Only explicitly named or interactively selected services start.
- PathPicker candidates still come only from the manifest. It does not trigger
  repository discovery implicitly.
- The startup message is exactly
  `Looming local services: user-svc, order-svc`.
- `localEnv` is injected for selected dependencies and `remoteEnv` for
  unselected dependencies.
- Logs from all selected services are available through `loom services --logs`. The
  boolean `--tail` switch provides the TTY dashboard and the non-TTY plain-text
  fallback described above.
- `services --status` reports saved PID/PGID data, and normal
  `services --stop` validates process identity.
- The picker toggles with `f`, an empty selection remains in the picker, only
  `y` or `yes` confirms startup, and non-TTY use without explicit services
  fails safely.
- A cyclic service fixture starts by dependency component and performs health
  checks only after all processes in that component have started.
- Dry-run does not create sessions, start processes, or open connections.
- In a generic fixture, set `runner.workdir` to the service source directory and
  set a templated `runner.runWorkdir` under
  `${runDir}/configs/${service}`. Have `prepare` create that directory. Confirm
  that prepare/build execute in `runner.workdir`, while the service process and
  a `command` health check execute in `runner.runWorkdir`. Also verify that a
  relative `runWorkdir` resolves from the service directory and that omitting it
  falls back to `runner.workdir`.
- Change only `runner.runWorkdir` to another prepare-created directory and run
  argument-free `services --restart`; the plan fingerprint must select and
  restart only that service. Then configure a run workdir that prepare does not
  create and force a restart. It must fail before stopping the existing service
  process.
- After changing one service's source or effective launch plan,
  `services --restart` with no service arguments restarts only that changed
  service. An explicit service argument forces its restart even when unchanged.
  Unchanged services, the shared connection, and the session log locations
  remain intact.
- `services --stop-all` and `services --stop --all` take the same cleanup path:
  both stop the full current session and release its connection lease. They
  terminate the owned ktctl connection only when no active workspace lease
  remains, and never terminate a connection still leased by another workspace.
  For an external ktctl process or network path recorded with both `Owned=false`
  and `Managed=false`, both forms remove only the session reference and never
  terminate the external connection.

In a disposable environment that supports `ktctl`, also verify readiness,
kubeconfig/context/namespace forwarding, shared connection leases, final-lease
shutdown, and manually reviewed orphan recovery. Set `KTCTL_KUBECONFIG` and
confirm that it overrides configured and manifest kubeconfig paths. With
`connection.sudo: true`, confirm the launched command is
`sudo -n <resolved-ktctl> --kubeconfig <file> ... connect`, after interactive
`sudo -v`; this also verifies that sudo does not depend on its own `secure_path`
to locate ktctl. Exercise both full-stop spellings and confirm the connection is
terminated after the final lease, while an active lease from another workspace
keeps it running. Also confirm that an external connection with both
`Owned=false` and `Managed=false` remains running after its session reference is
removed. Never use
`--force` until the saved PID/PGID has been verified.

After stable functional acceptance passes, retain a HEAD smoke test:

```bash
brew uninstall --formula "$LOOM_FORMULA"
brew install --formula --HEAD "$LOOM_FORMULA"
brew test --HEAD "$LOOM_FORMULA"
"$(brew --prefix "$LOOM_FORMULA")/bin/loom" --version
```

`brew reinstall` does not accept `--HEAD`, so the release test machine must
switch channels with uninstall/install. Restore and verify the stable release
afterward:

```bash
brew uninstall --formula "$LOOM_FORMULA"
brew install --formula --build-from-source "$LOOM_FORMULA"
brew test "$LOOM_FORMULA"
"$(brew --prefix "$LOOM_FORMULA")/bin/loom" --version
```

## 8. Complete the release

- Confirm CI passes for the source commit on the release branch, the tag, the
  Formula commit, and the final `origin/master`.
- Record the tag, source archive URL, and SHA-256 in the release notes.
- Confirm the English and Chinese README installation instructions describe the
  newly available stable Formula.
- Keep the tag immutable. Publish a patch release for any correction.
