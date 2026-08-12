# Releasing Conven

**English** | [简体中文](RELEASING-ZH.md)

This document covers Conven's Go source release, Homebrew Formula, and
precompiled Homebrew bottles.

## Release model

- Release source from `master` with an immutable annotated tag named
  `vX.Y.Z`.
- The stable Formula downloads the tagged source archive and verifies its
  SHA-256. CI builds and publishes bottles for supported macOS and Linux
  platforms; source fallback builds `./cmd/conven` with Go.
- Keep the `head` stanza so development builds remain available through
  `brew install --formula --HEAD leo1394/conven/conven`.
- Homebrew infers the version from the tag in the stable URL. Do not add a
  redundant `version` stanza.
- Never move, delete, or recreate a published tag. Fix a bad release with a new
  patch version.

The first stable release adds `url` and `sha256` after its tag exists. Later
releases replace those two values with the new tag archive.

Because the Formula lives in the same repository as the Go source, its final
SHA cannot be written into the source commit being hashed. The preferred bottle
release therefore has two phases:

1. Run `--prepare --bottle`, review its changes, then commit and push the
   prepared source to `master`. Besides the normal checks, this validates the
   bottle workflows.
2. Run `--apply --bottle`. It creates and pushes the source tag, finalizes the
   Formula on `bottle/conven-X.Y.Z`, opens a Formula pull request, waits for
   `brew test-bot`, dispatches `brew pr-pull` with the exact PR head SHA, waits
   for publication, synchronizes local `master`, and verifies the GitHub
   Release and bottle metadata.

`--bottle` is a modifier and is invalid without `--prepare` or `--apply`.
Commands without `--bottle` retain the source-build release path: plain
`--apply` commits the finalized Formula directly to `master` and publishes no
bottles.

If plain `--apply` already pushed the source tag for the same version, a later
`--apply --bottle` safely reuses that immutable annotated tag. If Formula
finalization had not started, it continues through the normal Formula branch.
If finalization also completed, the script verifies that `master` contains
exactly one Formula-only commit and that the complete Formula matches the
deterministic tag finalization, including its archive URL and SHA-256. It then
opens a Formula PR with temporary bottle bootstrap metadata; `brew pr-pull`
replaces that metadata with the published platform
checksums. The test and publish workflows both use `--keep-old` so this first
bottle publication for an existing version keeps bottle rebuild `0` while
updating the bootstrap block. The flow never moves or recreates the source tag.

`--finalize-formula` is a standalone manual and recovery action. It updates the
Formula but never stages, commits, or pushes it.

## Prerequisites

- Push access to `git@github.com:leo1394/homebrew-conven.git`.
- Go 1.23 or later, Homebrew, Ruby, Git, GitHub CLI (`gh`), ripgrep, `curl`,
  and `shasum`.
- GitHub CLI authenticated as a user that can create pull requests and dispatch
  Actions workflows in the repository.
- A clean local `master` synchronized with `origin/master`.
- A public GitHub repository and green CI on the current branch.

The repository Actions configuration must allow `GITHUB_TOKEN` to write
contents and pull requests, and any `master` ruleset must allow the publish
workflow to push the generated Formula/bottle commit. The `brew test-bot`
workflow runs on `macos-15` and `macos-26` (Apple Silicon), plus
`ubuntu-latest` (x86_64 Linux). Intel macOS currently falls back to a source
build because Homebrew's Go build dependency has no Intel bottle for the
standard macOS runner. Bottle publication uses GitHub Releases, not GHCR.

`ci.yml` remains the fast Go and stable/HEAD regression workflow.
`tests.yml` is the Homebrew packaging workflow. Both run on a Formula PR by
design; only `tests.yml` produces artifacts consumable by `brew pr-pull`, and
its push run performs syntax checks without publishing bottles.

Do not merge or close the generated Bottle PR while `publish.sh` is running.
The script publishes the artifacts first and Homebrew completes the Formula
update. If the PR was merged after successful test-bot jobs but before
publication, rerun the same command. Conven validates the merged PR base,
head, merge commit, unchanged bootstrap Formula, and successful workflow before
recovering the existing artifacts. Conflicting or partial release state is
rejected rather than overwritten. Recovery accepts the standard two-parent
GitHub merge commit; squash and rebase merges are rejected. A target/version
release lock also prevents two local `publish.sh` processes from dispatching
the same publication concurrently.

### One-time repository bootstrap

Complete this section before the normal preflight when `origin` does not exist.
Create the public repository and configure the remote:

```bash
git remote add origin git@github.com:leo1394/homebrew-conven.git
git remote -v
git status --short
```

Review every untracked file before staging it, commit the project baseline, and
push it:

```bash
git add --all
git diff --cached
git commit -m "Initialize conven"
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
CONVEN_RELEASE_VERSION=X.Y.Z
CONVEN_RELEASE_TAG="v$CONVEN_RELEASE_VERSION"
CONVEN_ARCHIVE_URL="https://github.com/leo1394/homebrew-conven/archive/refs/tags/$CONVEN_RELEASE_TAG.tar.gz"
CONVEN_ARCHIVE_PATH="/tmp/homebrew-conven-$CONVEN_RELEASE_VERSION.tar.gz"
CONVEN_FORMULA="leo1394/conven/conven"
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
git tag --list "$CONVEN_RELEASE_TAG"
git ls-remote --tags origin "refs/tags/$CONVEN_RELEASE_TAG"
```

Both tag lookup commands must print nothing.

## 1. Prepare the version

Update these locations:

| Location | Required change |
| --- | --- |
| `cmd/conven/main.go` | Set the `version` and `versionDate` variables |
| `VERSION.txt` | Write only `X.Y.Z` and a trailing newline |
| `CHANGELOG.md` | Add the release date and user-visible changes |
| `Formula/conven.rb` test | Expect the exact two-line version output for the HEAD build |
| `README.md` / `README-ZH.md` | Update only when installation or behavior changed |
| `RELEASING.md` / `RELEASING-ZH.md` | Keep both languages aligned when the release workflow changes |

Do not add the new stable archive URL or SHA yet. They can be known only after
the tag is published.

After updating `CHANGELOG.md` and any release-specific documentation, run the
preparation action from the repository root. The subshell keeps the current
working directory unchanged:

```bash
(
  cd ..
  ./publish.sh --target homebrew-conven --version "$CONVEN_RELEASE_VERSION" --prepare --bottle
)
```

The action updates the `version` and `versionDate` variables in
`cmd/conven/main.go`, `VERSION.txt`, and the Formula version assertion, runs the
release checks, and validates `.github/workflows/tests.yml` and
`.github/workflows/publish.yml`. It does not commit, tag, push, open a pull
request, or dispatch a workflow.

Check every version occurrence:

```bash
rg -n "$CONVEN_RELEASE_VERSION|version(Date)? =|assert_equal \"conven " \
  cmd/conven/main.go VERSION.txt CHANGELOG.md Formula/conven.rb README.md README-ZH.md
```

## 2. Run local checks

`--prepare --bottle` runs the Go and Formula checks below. Keep the full list available
for diagnosis and for the repository-specific example checks that are not part
of the generic publisher:

```bash
go mod tidy -diff
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build -o /tmp/conven-release ./cmd/conven
/tmp/conven-release --version
test -f examples/application.yaml
test ! -e examples/loom.yaml
test ! -e examples/conven.yaml
test -f internal/plugins/builtin/README.md
test ! -e internal/plugins/builtin/generate-apollo-consul.py

ruby -c Formula/conven.rb
brew style Formula/conven.rb

git diff --check
git diff
git status --short
```

The built binary must print the intended version, release date, and canonical
project URL. `conven --version`, `conven -v`, and `conven version` must produce
the same exact two-line output, for example:

```text
conven version X.Y.Z (YYYY-MM-DD)
https://github.com/leo1394/homebrew-conven
```

Review the diff rather than relying only on automated checks. In particular,
confirm that no credentials, runtime state, logs, or unrelated files are
included.

## 3. Commit and push the prepared source

Stage the required release files and any documentation changed for this
release:

```bash
git add cmd/conven/main.go VERSION.txt CHANGELOG.md Formula/conven.rb
git add README.md README-ZH.md RELEASING.md RELEASING-ZH.md
git diff --cached
git commit -m "Release conven $CONVEN_RELEASE_VERSION"
```

Omit unchanged documentation files from `git add`. For the first release, skip
the commit when the already-reviewed baseline is the exact release source.

Record the exact source SHA, push `master`, and verify that the remote branch
contains that exact commit:

```bash
CONVEN_SOURCE_COMMIT=$(git rev-parse HEAD)
git push origin master
git fetch origin master
test "$CONVEN_SOURCE_COMMIT" = "$(git rev-parse origin/master)"
```

Wait for CI on `$CONVEN_SOURCE_COMMIT` to pass before running `--apply --bottle`. Do not
change the working tree after this point. The apply preflight requires a clean
local `master`, synchronized `origin/master`, and no local or remote release
tag.

## 4. Publish the source tag and stable Formula

Run the apply action from the clean repository root:

```bash
(
  cd ..
  ./publish.sh --target homebrew-conven --version "$CONVEN_RELEASE_VERSION" --apply --bottle
)
```

For a bottle release, `--apply --bottle` performs the complete publication
sequence:

1. Repeats the release checks and verifies the clean, synchronized `master`.
2. Creates an annotated `vX.Y.Z` tag on `$CONVEN_SOURCE_COMMIT` and pushes it.
3. Downloads the public GitHub tag archive, verifies its top-level
   `VERSION.txt`, and calculates its SHA-256.
4. Creates `bottle/conven-X.Y.Z` from the source release, updates the stable
   Formula URL and checksum, validates it, commits it, and pushes the branch.
5. Opens a Formula pull request and records its number and exact head SHA.
6. Waits for every required `brew test-bot` job. The workflow builds and uploads
   platform-specific `*.bottle.*` artifacts only for the pull request.
7. Dispatches `.github/workflows/publish.yml` with the PR number and recorded
   head SHA. `brew pr-pull` rejects a changed PR head, publishes the bottles to
   a `conven-X.Y.Z` GitHub Release, applies the generated `bottle do` metadata,
   and pushes `master`.
8. Waits for the publish workflow, fast-forwards local `master`, and verifies
   the release assets and Formula bottle metadata.

If the command fails after creating the Formula PR, it deliberately preserves
the PR and `bottle/conven-X.Y.Z` branch. Fix or rerun from that state; do not
move the source tag or create a second PR for the same version.

Keep the existing Go build and completion generation. Do not copy the
single-file installation used by `homebrew-gits`, and do not make optional
tools such as `ktctl` or `sudo` hard Formula dependencies.

Verify the published result:

```bash
test "$(git rev-parse "$CONVEN_RELEASE_TAG^{commit}")" = "$CONVEN_SOURCE_COMMIT"
CONVEN_REMOTE_TAG_COMMIT=$(git ls-remote --tags origin \
  "refs/tags/$CONVEN_RELEASE_TAG^{}" | awk 'NR == 1 { print $1 }')
test "$CONVEN_REMOTE_TAG_COMMIT" = "$CONVEN_SOURCE_COMMIT"
git fetch origin master
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/master)"
grep -q '^  bottle do$' Formula/conven.rb
gh release view "conven-$CONVEN_RELEASE_VERSION"
git status --short
```

The final command must print nothing. Never move, delete, or recreate the
published tag. A source correction requires a new patch release.

### Bottle publication recovery

The bottle path is resumable. Inspect the immutable source tag, Formula branch,
pull request, and Actions runs before retrying:

```bash
git ls-remote --tags origin "refs/tags/$CONVEN_RELEASE_TAG" \
  "refs/tags/$CONVEN_RELEASE_TAG^{}"
git ls-remote --heads origin "refs/heads/bottle/conven-$CONVEN_RELEASE_VERSION"
gh pr list --head "bottle/conven-$CONVEN_RELEASE_VERSION" --state all
gh run list --workflow tests.yml --limit 10
gh run list --workflow publish.yml --limit 10
```

- Before the PR exists, rerun `--apply --bottle`; it reuses the verified source
  tag and Formula branch.
- If `brew test-bot` fails transiently, rerun the failed workflow and then rerun
  `--apply --bottle`. The Formula branch is deterministic and must not be edited
  by hand. A source or Formula defect requires a new patch release because the
  source tag is immutable.
- If test-bot is green but publication failed, rerun `--apply --bottle`; it
  dispatches or resumes `publish.yml` against the current verified PR head. If
  the failed run already uploaded part of the `conven-X.Y.Z` Release, remove
  that incomplete Release before retrying; GitHub rejects duplicate assets.
- If publication succeeded but the local checkout is stale, fast-forward
  `master` from `origin/master` and rerun verification. Do not republish assets
  or move the source tag.
- If `origin/master` already contains bottle metadata but the GitHub Release is
  incomplete or invalid, the script refuses to move the local checkout. Repair
  or remove that incomplete Release, then rerun `--apply --bottle`; successful
  verification will fast-forward the local `master` without moving the source
  tag.

### Source-build Formula finalization and failure recovery

Plain `--apply` cannot make the tag publication and later Formula commit atomic,
because GitHub must first materialize the tagged archive. If it fails, inspect
the tag, branch, and working tree before taking another action:

```bash
git tag --list "$CONVEN_RELEASE_TAG"
git ls-remote --tags origin "refs/tags/$CONVEN_RELEASE_TAG" \
  "refs/tags/$CONVEN_RELEASE_TAG^{}"
git status --short
```

- If no local or remote tag exists, correct the preflight failure and rerun
  `--apply`.
- If the verified source tag exists locally but not remotely, push that exact
  tag, then use `--finalize-formula`.
- If the remote tag exists but no Formula commit exists, keep the tag immutable
  and use `--finalize-formula` from a clean `master` synchronized with
  `origin/master`.
- If the Formula commit exists locally but its push failed, resolve any remote
  branch movement without force-pushing, then run `git push origin master`.

The standalone action reuses the archive validation and Formula update logic,
but deliberately does not stage, commit, or push:

```bash
(
  cd ..
  ./publish.sh --target homebrew-conven --version "$CONVEN_RELEASE_VERSION" --finalize-formula
)
git diff -- Formula/conven.rb
```

Review the diff and run the stable Formula gate below before manually completing
the recovery.

If a failed finalization already left `Formula/conven.rb` modified, inspect and
resolve that exact file first. Do not move the tag or force-push a rewritten
release.

### Stable Formula gate

The existing `ci.yml` installs and tests both the stable URL and HEAD Formula
path from the checkout. `tests.yml` additionally runs the official Homebrew
Formula lifecycle and produces bottle artifacts on Formula pull requests.
For a plain source-build `--apply`, or before manually committing a standalone
finalization, also run the following stricter stable Formula gate on a
disposable Homebrew test machine:

```bash
CONVEN_TEST_TAP="conven-release/conven"
CONVEN_TEST_FORMULA="$CONVEN_TEST_TAP/conven"
brew tap-new --no-git "$CONVEN_TEST_TAP"
CONVEN_TEST_TAP_DIR="$(brew --repository "$CONVEN_TEST_TAP")"
cp Formula/conven.rb "$CONVEN_TEST_TAP_DIR/Formula/conven.rb"

brew audit --formula --strict "$CONVEN_TEST_FORMULA"
brew install --formula --build-from-source "$CONVEN_TEST_FORMULA"
brew test "$CONVEN_TEST_FORMULA"
"$(brew --prefix "$CONVEN_TEST_FORMULA")/bin/conven" --version
```

The version must equal `conven $CONVEN_RELEASE_VERSION`. After the gate passes,
clean up the temporary test installation and tap:

```bash
brew uninstall --formula "$CONVEN_TEST_FORMULA"
brew untap "$CONVEN_TEST_TAP"
```

For standalone finalization, only after this gate passes, commit and push the
reviewed Formula explicitly:

```bash
git add Formula/conven.rb
git commit -m "update formula for $CONVEN_RELEASE_TAG"
CONVEN_FORMULA_COMMIT=$(git rev-parse HEAD)
git push origin master
```

The `GITHUB_TOKEN` push created by `brew pr-pull` does not trigger another push
workflow. Treat the successful publish workflow as the final gate, then
synchronize locally and verify the ancestry:

```bash
git switch master
git pull --ff-only origin master
git merge-base --is-ancestor "$CONVEN_SOURCE_COMMIT" origin/master
git merge-base --is-ancestor "$CONVEN_FORMULA_COMMIT" origin/master
git status --short
```

## 5. Verify the published artifact

Download the archive again after `--apply --bottle` and compare it with the
Formula:

```bash
curl -fL --retry 5 --retry-all-errors --retry-delay 2 \
  "$CONVEN_ARCHIVE_URL" -o "$CONVEN_ARCHIVE_PATH"
CONVEN_PUBLISHED_SHA=$(LC_ALL=C shasum -a 256 "$CONVEN_ARCHIVE_PATH" | awk '{print $1}')
FORMULA_SHA=$(ruby -ne 'puts $1 if $_ =~ /^\s*sha256 "([0-9a-f]{64})"/' Formula/conven.rb)
test "$CONVEN_PUBLISHED_SHA" = "$FORMULA_SHA"
```

Optionally build directly from the extracted archive to isolate source-package
problems from Homebrew:

```bash
CONVEN_ARCHIVE_DIR=$(mktemp -d /tmp/homebrew-conven-release.XXXXXX)
(
  tar -xzf "$CONVEN_ARCHIVE_PATH" -C "$CONVEN_ARCHIVE_DIR" --strip-components=1
  cd "$CONVEN_ARCHIVE_DIR"
  go test -count=1 ./...
  go build -o /tmp/conven-published ./cmd/conven
  /tmp/conven-published --version
)
```

## 6. Verify the Homebrew tap

Install the published Formula by its fully qualified name. This installs the tap
when needed and trusts only the requested Formula:

```bash
brew update
brew audit --formula --strict --online "$CONVEN_FORMULA"
brew fetch --formula --force-bottle "$CONVEN_FORMULA"
```

For a machine without Conven installed:

```bash
CONVEN_GO_BEFORE=$(brew list --versions go || true)
brew install --formula "$CONVEN_FORMULA"
test "$CONVEN_GO_BEFORE" = "$(brew list --versions go || true)"
```

For an existing stable or HEAD installation, switch explicitly rather than
using `reinstall`, which preserves the original install options:

```bash
brew uninstall --formula "$CONVEN_FORMULA"
brew install --formula "$CONVEN_FORMULA"
```

Do not use `--build-from-source` for this bottle acceptance check. If
`brew fetch --force-bottle` cannot select a bottle for the host, that platform
is not covered and a normal install will fall back to the Go build dependency.

Then verify the Formula and binary:

```bash
brew test "$CONVEN_FORMULA"
"$(brew --prefix "$CONVEN_FORMULA")/bin/conven" --version
brew info --formula "$CONVEN_FORMULA"
brew deps --formula --include-build "$CONVEN_FORMULA"
```

Verify all generated completions:

```bash
CONVEN_BASH_COMPLETION="$(brew --prefix)/etc/bash_completion.d/conven"
CONVEN_ZSH_COMPLETION="$(brew --prefix)/share/zsh/site-functions/_conven"
CONVEN_FISH_COMPLETION="$(brew --prefix)/share/fish/vendor_completions.d/conven.fish"
test -e "$CONVEN_BASH_COMPLETION"
test -e "$CONVEN_ZSH_COMPLETION"
test -e "$CONVEN_FISH_COMPLETION"
for completion in "$CONVEN_BASH_COMPLETION" "$CONVEN_ZSH_COMPLETION" "$CONVEN_FISH_COMPLETION"; do
  grep -Fq -- "services" "$completion"
  grep -Fq -- "plugins" "$completion"
done
for action in list registry status dashboard logs start restart stop stop-all cleanup; do
  for completion in "$CONVEN_BASH_COMPLETION" "$CONVEN_ZSH_COMPLETION"; do
    grep -Fq -- "--$action" "$completion"
  done
  grep -Fq -- "-l $action" "$CONVEN_FISH_COMPLETION"
done
for action in edit import reset; do
  for completion in "$CONVEN_BASH_COMPLETION" "$CONVEN_ZSH_COMPLETION"; do
    grep -Fq -- "--$action" "$completion"
  done
  grep -Fq -- "-l $action" "$CONVEN_FISH_COMPLETION"
done
for action in install list remove run; do
  for completion in "$CONVEN_BASH_COMPLETION" "$CONVEN_ZSH_COMPLETION"; do
    grep -Fq -- "--$action" "$completion"
  done
  grep -Fq -- "-l $action" "$CONVEN_FISH_COMPLETION"
done
for option in prune tail; do
  for completion in "$CONVEN_BASH_COMPLETION" "$CONVEN_ZSH_COMPLETION"; do
    grep -Fq -- "--$option" "$completion"
  done
  grep -Fq -- "-l $option" "$CONVEN_FISH_COMPLETION"
done
grep -Fq -- 'options="--tail --dashboard --help"' "$CONVEN_BASH_COMPLETION"
grep -Fq -- "'--dashboard[open the interactive log dashboard]'" "$CONVEN_ZSH_COMPLETION"
grep -Fq -- "__conven_services_action --logs' -l dashboard" "$CONVEN_FISH_COMPLETION"
! grep -Eq 'compgen -W "[^"]* (discover|start|restart|status|stop|logs|list)( |")' "$CONVEN_BASH_COMPLETION"
! grep -Eq "^[[:space:]]*'(discover|start|restart|status|stop|logs|list):" "$CONVEN_ZSH_COMPLETION"
! grep -Eq ' -a (discover|start|restart|status|stop|logs|list) -d ' "$CONVEN_FISH_COMPLETION"
! grep -Fq -- "--workspace" "$CONVEN_BASH_COMPLETION" "$CONVEN_ZSH_COMPLETION"
! grep -Fq -- "--config" "$CONVEN_BASH_COMPLETION" "$CONVEN_ZSH_COMPLETION"
! grep -Fq -- "-l workspace" "$CONVEN_FISH_COMPLETION"
! grep -Fq -- "-l config" "$CONVEN_FISH_COMPLETION"
```

## 7. Run functional acceptance

Use a disposable development workspace with no strong-match child repository
for the process checks below. In that case, `conven init` must create the fallback
manifest at `.conven/conven.yaml` from the bundled `examples/application.yaml`
template, and a second invocation must leave an existing manifest unchanged.
Edit that generated manifest and provide the referenced service repositories
before starting services. Make at least one selected acceptance service emit a
deterministic, non-ANSI startup log line so the redirected tail assertion has a
known fixture. Test repository discovery separately with generic disposable Git
repositories; do not encode a company-specific service stack as an automated
test. Do not run connection or process-recovery tests against production
infrastructure.

Before running the commands below, prepare
`.acceptance/import-candidate.yaml` as a complete valid Conven v1 manifest for the
same disposable service repositories. Make it differ from the `init` result in
at least `workspace.name`; it must contain no credentials. This exercises a real
replacement and backup rather than the byte-identical no-op path.

At minimum, verify:

```bash
CONVEN_BIN="$(brew --prefix "$CONVEN_FORMULA")/bin/conven"
CONVEN_TEST_KUBECONFIG=/absolute/path/to/disposable/kubeconfig
test -f "$CONVEN_TEST_KUBECONFIG"
"$CONVEN_BIN" init
test -f .conven/conven.yaml
CONVEN_MANIFEST_SHA=$(shasum -a 256 .conven/conven.yaml | awk '{print $1}')
"$CONVEN_BIN" init
test "$CONVEN_MANIFEST_SHA" = "$(shasum -a 256 .conven/conven.yaml | awk '{print $1}')"
mkdir -p .acceptance/descendant
CONVEN_IMPORT_SOURCE=.acceptance/import-candidate.yaml
CONVEN_IMPORT_SOURCE_SHA=$(shasum -a 256 "$CONVEN_IMPORT_SOURCE" | awk '{print $1}')
(cd .acceptance/descendant && "$CONVEN_BIN" policy --import ../import-candidate.yaml)
test "$CONVEN_IMPORT_SOURCE_SHA" = "$(shasum -a 256 "$CONVEN_IMPORT_SOURCE" | awk '{print $1}')"
cmp "$CONVEN_IMPORT_SOURCE" .conven/conven.yaml
test -n "$(find .conven/backups -type f -name 'conven.yaml-before-import-*.bak' -print -quit)"
"$CONVEN_BIN" config ktctl.path ktctl
test "$("$CONVEN_BIN" config ktctl.path)" = "ktctl"
"$CONVEN_BIN" config ktctl.kubeconfig "$CONVEN_TEST_KUBECONFIG"
test "$("$CONVEN_BIN" config ktctl.kubeconfig)" = "$CONVEN_TEST_KUBECONFIG"
"$CONVEN_BIN" config --list
"$CONVEN_BIN" doctor
"$CONVEN_BIN" services --start --dry-run user-svc order-svc
"$CONVEN_BIN" services --list
(cd .acceptance/descendant && "$CONVEN_BIN" services --list)
(cd .acceptance/descendant && "$CONVEN_BIN" services --registry)
CONVEN_WORKSPACE=/path/that/is/not/a/workspace "$CONVEN_BIN" services --list
for command in services doctor; do
  for removed_flag in --workspace --config; do
    if "$CONVEN_BIN" "$command" "$removed_flag" /tmp >/dev/null 2>&1; then
      false
    else
      CONVEN_USAGE_STATUS=$?
      test "$CONVEN_USAGE_STATUS" -eq 2
    fi
  done
done
CONVEN_HELP_OUTPUT=$(mktemp /tmp/conven-help.XXXXXX)
for command in services doctor; do
  "$CONVEN_BIN" "$command" --help >"$CONVEN_HELP_OUTPUT"
  ! grep -Fq -- "--workspace" "$CONVEN_HELP_OUTPUT"
  ! grep -Fq -- "--config" "$CONVEN_HELP_OUTPUT"
done
"$CONVEN_BIN" services --logs --help >"$CONVEN_HELP_OUTPUT"
grep -Fq -- "--tail" "$CONVEN_HELP_OUTPUT"
grep -Fq -- "--dashboard" "$CONVEN_HELP_OUTPUT"
grep -Fq -- "last --tail or --dashboard flag wins" "$CONVEN_HELP_OUTPUT"
for removed_command in discover start restart status stop logs list; do
  if "$CONVEN_BIN" "$removed_command" --help >/dev/null 2>&1; then
    false
  else
    CONVEN_USAGE_STATUS=$?
    test "$CONVEN_USAGE_STATUS" -eq 2
  fi
done
if "$CONVEN_BIN" services --tail --logs user-svc >/dev/null 2>&1; then
  false
else
  CONVEN_USAGE_STATUS=$?
  test "$CONVEN_USAGE_STATUS" -eq 2
fi
rm -f "$CONVEN_HELP_OUTPUT"
"$CONVEN_BIN" services --start --dry-run --dev user-svc order-svc
"$CONVEN_BIN" services --start --dry-run user-svc order-svc
"$CONVEN_BIN" services --start user-svc order-svc </dev/null
"$CONVEN_BIN" services --status
"$CONVEN_BIN" services --logs user-svc order-svc
CONVEN_TAIL_OUTPUT=$(mktemp /tmp/conven-tail.XXXXXX)
"$CONVEN_BIN" services --logs --tail user-svc order-svc >"$CONVEN_TAIL_OUTPUT" &
CONVEN_TAIL_PID=$!
sleep 1
kill -INT "$CONVEN_TAIL_PID"
wait "$CONVEN_TAIL_PID"
grep -Eq '^\[(user-svc|order-svc)\] ' "$CONVEN_TAIL_OUTPUT"
! LC_ALL=C grep -Fq "$(printf '\033[?1049h')" "$CONVEN_TAIL_OUTPUT"
! LC_ALL=C grep -Fq "$(printf '\033[2J')" "$CONVEN_TAIL_OUTPUT"
rm -f "$CONVEN_TAIL_OUTPUT"
# Edit a tracked source file in one running service before this automatic check.
"$CONVEN_BIN" services --restart
"$CONVEN_BIN" services --restart user-svc
"$CONVEN_BIN" services --stop-all
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
  `conven services --registry`. The new path is added while the existing service
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
  symbolic-link `.conven/conven.yaml` must also be rejected rather than replaced.

The redirected `services --logs --tail` invocation above is the non-TTY Plain
acceptance path. It must remain a continuous `[service]`-prefixed stream and
must not emit Dashboard control sequences. ANSI bytes written by a service may
remain in this Plain stream; sanitization applies only to full-screen Dashboard
rendering. The preceding start uses non-TTY standard input intentionally: a
successful start must return and leave the services running instead of entering
either viewer.

Also exercise the dashboard from a real interactive terminal rather than a
redirect or pipeline:

```bash
"$CONVEN_BIN" services --start user-svc order-svc
# Start opens the Dashboard by default. Exercise its controls, then press q.
"$CONVEN_BIN" services --status
"$CONVEN_BIN" services --restart user-svc
# Restart opens the Dashboard by default. Exercise its controls, then press q.
"$CONVEN_BIN" services --dashboard user-svc order-svc
# Press Ctrl-C. The restarted service and unchanged service must remain running.
"$CONVEN_BIN" services --status
"$CONVEN_BIN" services --logs --tail --dashboard user-svc order-svc
# The last mode flag selects the Dashboard alias; exercise it, then press q.
"$CONVEN_BIN" services --logs --dashboard --tail user-svc order-svc
# The last mode flag selects Plain; use native scrollback and Command+F, then press Ctrl-C.
"$CONVEN_BIN" services --stop --all
```

Confirm that `services --logs --tail`, `services --start --tail`, and
`services --restart --tail` use the Plain continuous stream even in a TTY;
`--tail` remains a boolean switch rather than a line-count option. Plain output
must enter native terminal scrollback and be searchable with Command+F.
`services --logs --dashboard` must behave exactly like the independent
`services --dashboard` action. Restart also accepts `--dashboard` and defaults
to the Dashboard on a usable TTY. When both mode flags are supplied to logs or
restart, confirm that the last occurrence wins: `--tail --dashboard` opens the
Dashboard and `--dashboard --tail` selects Plain.

The full-screen Dashboard must keep a fixed banner for the
workspace/environment, selected running services, their manifest-declared port
snapshots, and the current LAN IPv4 address. Its application-owned history must
retain at most 10,000 received lines. Verify mouse-wheel, Up/Down, and PgUp/PgDn
scrolling, Home or `g` for the oldest retained line, End or `G` for live follow,
`/` search, `n`/`N` match navigation, and Esc to clear search. Terminal
Command+F is not the Dashboard history search and need only see the currently
rendered frame. Use a JSON log longer than the terminal width and verify that it
wraps into scrollable visual rows without a viewport ellipsis or lost content.
The top rule and field/protocol labels must be white, only the numeric local
service count green, and the centered control hint yellow; the banner must not
use a background color.
Resizing the terminal must rewrap and redraw the layout, and ANSI or other
terminal control sequences written by a service must be sanitized. Both `q`
and `Ctrl-C` must only detach, restore the terminal, and leave all services
running, as verified by the following `services --status`.
The displayed ports are configuration snapshots rather than live listening-
socket probes; the LAN address and a displayed port do not by themselves prove
that an endpoint is bound or reachable.

Confirm the following behavior:

- `services` and `doctor` resolve only the nearest `.conven/conven.yaml` by walking
  upward from the current directory. A root-level `conven.yaml` and alternate
  files inside `.conven` are ignored. A nested `.conven` without `conven.yaml` is an
  incomplete hard boundary
  and must fail instead of falling back to a valid parent workspace. Repository
  discovery always scans immediate children of the resolved workspace root,
  never children of the invocation directory.
- The `services` action must be its first argument, and exactly one of `--list`,
  `--registry`, `--status`, `--dashboard`, `--logs`, `--start`, `--restart`,
  `--stop`, `--stop-all`, or `--cleanup` is required. The former top-level
  service commands return usage status 2 and are absent from help and generated
  top-level completion candidates.
- The removed workspace and manifest flags return usage status 2 for every
  workspace command and are absent from command help and all generated
  completions. Setting `CONVEN_WORKSPACE` cannot change CLI discovery: a command
  inside a workspace still uses that workspace, and a command outside one still
  fails.
- Outside a workspace, `init`, `config --global`, help, version, command-specific
  help, and internal completion generation remain available. Workspace-local
  `config` and service commands fail with a clear initialization error. Local
  `config` works at a `.conven` boundary while `conven.yaml` is still being prepared.
  The user-wide `~/.conven` directory is always reserved for global settings: it
  must not make the home directory or its descendants a workspace, even if a
  manifest was created there manually. `conven init` must reject the home directory.
- Every launched local service receives the resolved absolute workspace root in
  `CONVEN_WORKSPACE`, overriding an inherited value. Treat it as read-only service
  metadata; it is not an input to CLI discovery.
- Local settings are stored in `.conven/config`, global settings in
  `~/.conven/config`, and local `ktctl.path` and `ktctl.kubeconfig` values override
  global values. Remove both temporary local values with `conven config --unset`
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
  `Convening local services: user-svc, order-svc`.
- `localEnv` is injected for selected dependencies and `remoteEnv` for
  unselected dependencies.
- Logs from all selected services are available through `conven services --logs`.
  `services --dashboard` and its `services --logs --dashboard` alias provide the
  fixed-banner TTY viewer and its application-owned 10,000-line history;
  `services --logs --tail` provides the Plain native-scrollback stream. If both
  logs or restart mode flags appear, the last one wins. Interactive start and
  restart default to the Dashboard, explicit `--tail` selects Plain, and their
  non-TTY default returns after completing. Long Dashboard logs wrap into
  scrollable visual rows without losing the original searchable logical line.
- `services --status` reports saved PID/PGID data, and normal
  `services --stop` validates process identity.
- The picker toggles the current service with `f` and all services with `A`;
  an empty selection remains in the picker. Confirmation accepts `y`/`yes` to
  continue and `n`/`no` to cancel, and retries invalid input at most three
  times. Non-TTY use without explicit services fails safely.
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
- After `services --stop-all`, `services --cleanup` removes only
  `runtime/current/artifacts` and `runtime/current/logs`. It preserves runtime
  configs and the shared connection log, and refuses while a session is saved
  or when a cleanup target is a symlink or escapes the workspace runtime.

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
brew uninstall --formula "$CONVEN_FORMULA"
brew install --formula --HEAD "$CONVEN_FORMULA"
brew test --HEAD "$CONVEN_FORMULA"
"$(brew --prefix "$CONVEN_FORMULA")/bin/conven" --version
```

`brew reinstall` does not accept `--HEAD`, so the release test machine must
switch channels with uninstall/install. Restore and verify the stable release
afterward:

```bash
brew uninstall --formula "$CONVEN_FORMULA"
brew install --formula "$CONVEN_FORMULA"
brew test "$CONVEN_FORMULA"
"$(brew --prefix "$CONVEN_FORMULA")/bin/conven" --version
```

## 8. Complete the release

- Confirm CI passes for the prepared source commit, Formula PR, every test-bot
  platform, and the `brew pr-pull` workflow. Confirm the final `origin/master`
  Formula and bottle installation with the verification gates above.
- Record the source tag, archive URL and SHA-256, Formula PR, and
  `conven-X.Y.Z` bottle Release in the release notes.
- Confirm the English and Chinese README installation instructions describe the
  newly available stable Formula.
- Keep the tag immutable. Publish a patch release for any correction.
