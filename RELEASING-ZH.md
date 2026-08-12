# 发布 Conven

[English](RELEASING.md) | **简体中文**

本文说明 Conven 的 Go 源码、Homebrew Formula 和预编译 Homebrew bottle 发布流程。

## 发布模型

- 从 `master` 发布源码，使用不可变 annotated tag `vX.Y.Z`。
- 稳定 Formula 下载带 tag 的源码归档并校验 SHA-256。CI 为支持的 macOS 和
  Linux 平台构建并发布 bottle；源码回退路径使用 Go 构建 `./cmd/conven`。
- 保留 `head`，开发版本仍可通过
  `brew install --formula --HEAD leo1394/conven/conven` 安装。
- Homebrew 会从稳定 URL 中的 tag 推断版本，不要重复声明 `version`。
- 已发布 tag 不得移动、删除或重建；错误必须通过新的 patch 版本修复。

首次稳定发布需要在 tag 存在后添加 `url` 和 `sha256`；后续发布则把这两个值替换为
新的 tag 归档。

Formula 和 Go 源码位于同一个仓库，因此不能把最终 SHA 写入被该 SHA 校验的源码
commit。推荐的 bottle 发布分为两个阶段：

1. 执行 `--prepare --bottle`，审阅修改，然后把准备好的源码提交并推送到
   `master`。除常规检查外，该动作还会校验 bottle workflow。
2. 执行 `--apply --bottle`。它会创建并推送源码 tag，在
   `bottle/conven-X.Y.Z` 上完成 Formula，创建 Formula PR，等待
   `brew test-bot`，使用准确的 PR head SHA 调度 `brew pr-pull`，等待发布完成，
   同步本地 `master`，并验证 GitHub Release 和 bottle 元数据。

`--bottle` 是修饰参数，不能脱离 `--prepare` 或 `--apply` 单独使用。不带
`--bottle` 的命令保留源码构建发布路径：普通 `--apply` 仍将完成后的 Formula
直接提交到 `master`，但不会发布 bottle。

如果同一版本此前普通 `--apply` 已经推送源码 tag，后续的 `--apply --bottle` 会安全
复用该不可变 annotated tag。若 Formula 尚未完成，则继续走正常 Formula 分支；若
Formula 也已完成，脚本会确认 `master` 只包含一个 Formula 提交，并校验完整 Formula
与 tag 的确定性完成结果一致，包括归档 URL 和 SHA-256；随后通过 Formula PR 加入
临时 bottle bootstrap 元数据，由 `brew pr-pull` 替换为已发布平台的校验和。测试与
发布 workflow 都使用 `--keep-old`，确保已有版本首次补发 bottle 时保持 bottle
rebuild 为 `0`，同时更新临时 block。该流程不会移动或重建源码 tag。

`--finalize-formula` 是独立的手动及故障恢复动作。它只修改 Formula，绝不会暂存、
提交或推送。

## 前置条件

- 拥有 `git@github.com:leo1394/homebrew-conven.git` 的推送权限。
- 已安装 Go 1.23 或更高版本、Homebrew、Ruby、Git、GitHub CLI（`gh`）、ripgrep、
  `curl` 和 `shasum`。
- GitHub CLI 已登录，并且当前用户可以在仓库中创建 PR 和调度 Actions workflow。
- 本地 `master` 干净，并与 `origin/master` 同步。
- GitHub 仓库已公开，当前分支 CI 全部通过。

仓库 Actions 设置必须允许 `GITHUB_TOKEN` 写入 contents 和 pull requests；如果
`master` 存在 ruleset，还必须允许 publish workflow 推送生成的 Formula/bottle commit。
`brew test-bot` workflow 运行于 `macos-15`、`macos-26`（Apple Silicon）和
`ubuntu-latest`（x86_64 Linux）。由于 Homebrew 的 Go 构建依赖当前没有适用于
标准 macOS Intel runner 的 bottle，Intel macOS 暂时回退为源码构建。Bottle
发布到 GitHub Releases，不使用 GHCR。

`ci.yml` 保留为 Go 及 stable/HEAD 的快速回归 workflow；`tests.yml` 负责 Homebrew
打包。Formula PR 会有意同时运行二者；只有 `tests.yml` 生成可供 `brew pr-pull`
使用的 artifact，它在 push 事件中只做语法检查，不发布 bottle。

`publish.sh` 运行期间不要手动合并或关闭生成的 Bottle PR。脚本会先发布 artifacts，
再由 Homebrew 完成 Formula 更新。如果 test-bot 已成功、但 PR 在发布前被合并，重新
执行相同命令即可。Conven 会验证该 PR 的 base、head、merge commit、未被修改的
bootstrap Formula 和成功 workflow，再复用现有 artifacts；遇到冲突或不完整 Release
时会拒绝覆盖。恢复仅接受 GitHub 标准的双亲 merge commit，squash/rebase merge 会被
拒绝。target/version 级发布锁也会阻止两个本地 `publish.sh` 进程并发派发同一次发布。

### 一次性仓库初始化

如果还没有 `origin`，必须先完成本节，再执行通用预检。创建公开仓库并配置远端：

```bash
git remote add origin git@github.com:leo1394/homebrew-conven.git
git remote -v
git status --short
```

逐项检查未跟踪文件后再暂存，提交项目基线并推送：

```bash
git add --all
git diff --cached
git commit -m "Initialize conven"
git push -u origin master
```

不要提交本地凭据、kubeconfig、运行状态或服务日志。如果基线 commit 已经是首个版本
的准确源码，可在全部检查通过后直接为它创建 tag，不需要空的发布 commit。

### 发布变量

在预检前选择语义化版本，并在整个流程中复用以下发布专用变量。下面的值只是示例：

```bash
CONVEN_RELEASE_VERSION=X.Y.Z
CONVEN_RELEASE_TAG="v$CONVEN_RELEASE_VERSION"
CONVEN_ARCHIVE_URL="https://github.com/leo1394/homebrew-conven/archive/refs/tags/$CONVEN_RELEASE_TAG.tar.gz"
CONVEN_ARCHIVE_PATH="/tmp/homebrew-conven-$CONVEN_RELEASE_VERSION.tar.gz"
CONVEN_FORMULA="leo1394/conven/conven"
```

### 每次发布的预检

完成一次性初始化后，同步 `master`：

```bash
git switch master
git pull --ff-only origin master
git status --short
```

最后一条命令必须没有输出。拉取 tag，并确认所选 tag 在本地和远端都不存在：

```bash
git fetch origin --tags
git tag --list "$CONVEN_RELEASE_TAG"
git ls-remote --tags origin "refs/tags/$CONVEN_RELEASE_TAG"
```

两条 tag 查询命令都必须没有输出。

## 1. 准备版本

更新以下位置：

| 位置 | 必须修改的内容 |
| --- | --- |
| `cmd/conven/main.go` | 设置 `version` 和 `versionDate` 变量 |
| `VERSION.txt` | 仅保留 `X.Y.Z` 和末尾换行 |
| `CHANGELOG.md` | 添加发布日期及用户可见变化 |
| `Formula/conven.rb` test | HEAD 构建应断言准确的两行版本输出 |
| `README.md` / `README-ZH.md` | 仅在安装方式或行为变化时更新 |
| `RELEASING.md` / `RELEASING-ZH.md` | 发布流程变化时保持中英文同步 |

此时不要添加新版本的稳定归档 URL 或 SHA；只有发布 tag 后才能确定这些值。

更新 `CHANGELOG.md` 和本次发布涉及的文档后，从仓库根目录执行准备动作。子 shell 不会
改变当前工作目录：

```bash
(
  cd ..
  ./publish.sh --target homebrew-conven --version "$CONVEN_RELEASE_VERSION" --prepare --bottle
)
```

该动作更新 `cmd/conven/main.go` 中的 `version` 和 `versionDate` 变量、`VERSION.txt`
以及 Formula 版本断言，执行发布检查，并校验 `.github/workflows/tests.yml` 和
`.github/workflows/publish.yml`；不会提交、创建 tag、推送、创建 PR 或调度 workflow。

检查所有版本位置：

```bash
rg -n "$CONVEN_RELEASE_VERSION|version(Date)? =|assert_equal \"conven " \
  cmd/conven/main.go VERSION.txt CHANGELOG.md Formula/conven.rb README.md README-ZH.md
```

## 2. 本地检查

`--prepare --bottle` 会执行下列 Go 和 Formula 检查。保留完整命令，便于诊断，以及执行通用发布
脚本未包含的仓库专用 example 检查：

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

构建出的程序必须输出目标版本、发布日期和规范项目 URL。`conven --version`、
`conven -v` 与 `conven version` 必须输出完全相同的两行内容，例如：

```text
conven version X.Y.Z (YYYY-MM-DD)
https://github.com/leo1394/homebrew-conven
```

不能只依赖自动检查，还要人工审阅 diff，确认其中没有凭据、运行状态、日志或无关文件。

## 3. 提交并推送准备好的源码

暂存必须的发布文件，以及本次确实发生变化的文档：

```bash
git add cmd/conven/main.go VERSION.txt CHANGELOG.md Formula/conven.rb
git add README.md README-ZH.md RELEASING.md RELEASING-ZH.md
git diff --cached
git commit -m "Release conven $CONVEN_RELEASE_VERSION"
```

没有变化的文档不要加入 `git add`。首次发布时，如果已审阅的基线就是准确的发布
源码，可跳过该 commit。

记录准确的源码 SHA，推送 `master`，并验证远程分支包含该准确 commit：

```bash
CONVEN_SOURCE_COMMIT=$(git rev-parse HEAD)
git push origin master
git fetch origin master
test "$CONVEN_SOURCE_COMMIT" = "$(git rev-parse origin/master)"
```

等待 `$CONVEN_SOURCE_COMMIT` 的 CI 通过后再执行 `--apply --bottle`。此后不要修改工作树。apply
预检要求本地 `master` 干净、与 `origin/master` 同步，并且本地和远程都不存在发布
tag。

## 4. 发布源码 tag 和稳定 Formula

从干净的仓库根目录执行 apply 动作：

```bash
(
  cd ..
  ./publish.sh --target homebrew-conven --version "$CONVEN_RELEASE_VERSION" --apply --bottle
)
```

对于 bottle 发布，`--apply --bottle` 会执行完整发布顺序：

1. 再次运行发布检查，并验证干净且同步的 `master`。
2. 在 `$CONVEN_SOURCE_COMMIT` 上创建 annotated `vX.Y.Z` tag 并推送。
3. 下载 GitHub 公开 tag 归档，验证顶层 `VERSION.txt` 并计算 SHA-256。
4. 从源码发布创建 `bottle/conven-X.Y.Z`，更新稳定 Formula URL 和校验和，校验、
   提交并推送该分支。
5. 创建 Formula PR，并记录 PR 编号及准确的 head SHA。
6. 等待全部 `brew test-bot` job。该 workflow 只在 PR 上构建并上传各平台的
   `*.bottle.*` artifact。
7. 使用 PR 编号和已记录的 head SHA 调度 `.github/workflows/publish.yml`。
   `brew pr-pull` 会拒绝已经变化的 PR head，把 bottle 发布到
   `conven-X.Y.Z` GitHub Release，应用生成的 `bottle do` 元数据，并推送
   `master`。
8. 等待 publish workflow 完成，快进本地 `master`，并验证 Release asset 和
   Formula bottle 元数据。

如果命令在 Formula PR 创建后失败，会有意保留 PR 和 `bottle/conven-X.Y.Z`
分支。应从该状态修复或重试；不得移动源码 tag，也不要为同一版本创建第二个 PR。

保留已有 Go 构建和补全生成逻辑。不要复制 `homebrew-gits` 的单文件安装方式，也不要
把可选工具 `ktctl` 或 `sudo` 设为 Formula 强依赖。

验证发布结果：

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

最后一个命令必须没有输出。已经发布的 tag 绝不能移动、删除或重建；源码修正必须发布
新的 patch 版本。

### Bottle 发布故障恢复

Bottle 路径可以续跑。重试前检查不可变源码 tag、Formula 分支、PR 和 Actions run：

```bash
git ls-remote --tags origin "refs/tags/$CONVEN_RELEASE_TAG" \
  "refs/tags/$CONVEN_RELEASE_TAG^{}"
git ls-remote --heads origin "refs/heads/bottle/conven-$CONVEN_RELEASE_VERSION"
gh pr list --head "bottle/conven-$CONVEN_RELEASE_VERSION" --state all
gh run list --workflow tests.yml --limit 10
gh run list --workflow publish.yml --limit 10
```

- PR 尚未创建时，重新执行 `--apply --bottle`；它会复用已验证的源码 tag 和
  Formula 分支。
- `brew test-bot` 因瞬时问题失败时，重新运行失败的 workflow，再重试
  `--apply --bottle`。Formula 分支由脚本确定性生成，不应手工修改；如果源码或
  Formula 本身有缺陷，由于源码 tag 不可变，必须发布新的 patch 版本。
- test-bot 已通过但发布失败时，重新执行 `--apply --bottle`；它会针对当前已验证的
  PR head 调度或续接 `publish.yml`。如果失败的 run 已上传部分
  `conven-X.Y.Z` Release，重试前先删除该不完整 Release；GitHub 会拒绝重名 asset。
- 发布已经成功但本地 checkout 落后时，从 `origin/master` 快进 `master` 后重新
  验证。不要重复发布 asset，也不要移动源码 tag。
- 如果 `origin/master` 已包含 bottle metadata，但 GitHub Release 不完整或无效，
  脚本不会移动本地 checkout。修复或删除该不完整 Release 后重新执行
  `--apply --bottle`；验证通过后会安全快进本地 `master`，且不会移动源码 tag。

### 源码构建 Formula 完成及故障恢复

普通 `--apply` 无法让 tag 发布与之后的 Formula commit 成为原子操作，因为 GitHub 必须先
生成带 tag 的归档。如果执行失败，采取后续动作前先检查 tag、分支和工作树：

```bash
git tag --list "$CONVEN_RELEASE_TAG"
git ls-remote --tags origin "refs/tags/$CONVEN_RELEASE_TAG" \
  "refs/tags/$CONVEN_RELEASE_TAG^{}"
git status --short
```

- 如果本地和远程都没有 tag，修复预检问题后重新执行 `--apply`。
- 如果已验证的源码 tag 只存在于本地，先推送这个准确 tag，再执行
  `--finalize-formula`。
- 如果远程 tag 已存在但 Formula commit 不存在，保持 tag 不变，在干净且与
  `origin/master` 同步的 `master` 上执行 `--finalize-formula`。
- 如果本地已有 Formula commit 但推送失败，在不强制推送的前提下处理远程分支变化，
  然后执行 `git push origin master`。

独立动作会复用归档验证和 Formula 更新逻辑，但特意不暂存、不提交也不推送：

```bash
(
  cd ..
  ./publish.sh --target homebrew-conven --version "$CONVEN_RELEASE_VERSION" --finalize-formula
)
git diff -- Formula/conven.rb
```

审阅 diff，并执行下方稳定 Formula 门禁，然后再手动完成故障恢复。

如果失败的 Formula 完成过程已经修改 `Formula/conven.rb`，先检查并只处理该文件。不要
移动 tag，也不要强制推送改写后的发布。

### 稳定 Formula 门禁

现有 `ci.yml` 会从 checkout 同时安装并测试稳定 URL 和 HEAD Formula；`tests.yml`
还会在 Formula PR 上执行 Homebrew 官方 Formula 生命周期并生成 bottle artifact。
对于普通源码构建 `--apply`，或手动提交独立完成结果前，还应在一次性 Homebrew
测试机上执行以下更严格的稳定 Formula 门禁：

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

版本必须等于 `conven $CONVEN_RELEASE_VERSION`。门禁通过后，清理临时测试安装和 tap：

```bash
brew uninstall --formula "$CONVEN_TEST_FORMULA"
brew untap "$CONVEN_TEST_TAP"
```

独立完成 Formula 时，只有该门禁通过后，才手动提交并推送已审阅的 Formula：

```bash
git add Formula/conven.rb
git commit -m "update formula for $CONVEN_RELEASE_TAG"
CONVEN_FORMULA_COMMIT=$(git rev-parse HEAD)
git push origin master
```

`brew pr-pull` 使用 `GITHUB_TOKEN` 推送后不会再次触发 push workflow。将成功的
publish workflow 作为最终门禁，然后同步本地并检查祖先关系：

```bash
git switch master
git pull --ff-only origin master
git merge-base --is-ancestor "$CONVEN_SOURCE_COMMIT" origin/master
git merge-base --is-ancestor "$CONVEN_FORMULA_COMMIT" origin/master
git status --short
```

## 5. 验证公开产物

`--apply --bottle` 后重新下载归档，并与 Formula 比较：

```bash
curl -fL --retry 5 --retry-all-errors --retry-delay 2 \
  "$CONVEN_ARCHIVE_URL" -o "$CONVEN_ARCHIVE_PATH"
CONVEN_PUBLISHED_SHA=$(LC_ALL=C shasum -a 256 "$CONVEN_ARCHIVE_PATH" | awk '{print $1}')
FORMULA_SHA=$(ruby -ne 'puts $1 if $_ =~ /^\s*sha256 "([0-9a-f]{64})"/' Formula/conven.rb)
test "$CONVEN_PUBLISHED_SHA" = "$FORMULA_SHA"
```

也可直接从解压后的归档构建，以便把源码包问题和 Homebrew 问题分开：

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

## 6. 验证 Homebrew tap

使用完整名称安装已发布的 Formula。需要时该命令会自动安装 tap，并且只信任请求的
Formula：

```bash
brew update
brew audit --formula --strict --online "$CONVEN_FORMULA"
brew fetch --formula --force-bottle "$CONVEN_FORMULA"
```

未安装 Conven 时：

```bash
CONVEN_GO_BEFORE=$(brew list --versions go || true)
brew install --formula "$CONVEN_FORMULA"
test "$CONVEN_GO_BEFORE" = "$(brew list --versions go || true)"
```

已安装稳定版或 HEAD 时，应显式切换，不要使用会保留原安装选项的 `reinstall`：

```bash
brew uninstall --formula "$CONVEN_FORMULA"
brew install --formula "$CONVEN_FORMULA"
```

Bottle 验收不能使用 `--build-from-source`。如果 `brew fetch --force-bottle` 无法为
当前主机选择 bottle，说明该平台尚未覆盖，普通安装会回退源码构建并安装 Go 构建依赖。

然后验证 Formula 和程序：

```bash
brew test "$CONVEN_FORMULA"
"$(brew --prefix "$CONVEN_FORMULA")/bin/conven" --version
brew info --formula "$CONVEN_FORMULA"
brew deps --formula --include-build "$CONVEN_FORMULA"
```

验证三种补全：

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

## 7. 功能验收

下面的进程验收使用没有强匹配一级子仓库的一次性开发 workspace。此时 `conven init`
必须根据内置的 `examples/application.yaml` 模板创建回退 manifest
`.conven/conven.yaml`，再次执行时必须保留已有 manifest。启动服务前，应编辑生成的
manifest，并准备其中引用的服务仓库。至少让一个选中的验收服务在启动时输出一条
确定且不带 ANSI 的日志，使重定向 tail 断言拥有已知 fixture。仓库发现应另用通用的
一次性 Git 仓库验收，不要把公司业务服务栈写成自动测试。不要对生产基础设施执行
连接或进程恢复测试。

执行下面命令前，先准备 `.acceptance/import-candidate.yaml`：它必须是适用于同一组
一次性服务仓库的完整、合法 Conven v1 manifest，不含凭据，并且至少让
`workspace.name` 与 `init` 结果不同。这样才能覆盖真实替换和备份路径，而不是逐字节
相同的 no-op。

至少验证：

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
# 在执行下面的自动检查前，修改一个运行中服务的已跟踪源码文件。
"$CONVEN_BIN" services --restart
"$CONVEN_BIN" services --restart user-svc
"$CONVEN_BIN" services --stop-all
```

Import 验收必须确认：相对路径从 `.acceptance/descendant` 解析；源文件 hash 不变；规范
manifest 与完整候选逐字节一致，而不是字段 merge；旧目标已备份。还要单独测试无效
候选：不带 `--edit` 时不得修改源文件或目标；配合一个会等待退出的确定性编辑器时，
`--import ... --edit` 只能编辑私有草稿，可以发布修复后的有效结果，但源文件仍不变。
Schema 校验通过不是运行门禁，实际启动前仍必须通过上面的 doctor 和 start dry-run。

另建一个一次性 workspace，用通用的一级子 Git 仓库验收发现功能。强匹配 fixture
必须包含 `go/go.mod`、声明 `package main` 的 `go/main.go`，并且 module path 最后一段
与仓库目录名相同。不得使用真实业务仓库，并确认：

- 首次 `init` 用全部强匹配替换示例 service，把 `path` 设为仓库名、
  `runner.workdir` 设为 `go`、`runner.build` 设为 argv
  `[go, build, -o, "${artifact}", .]`，并把 `runner.run` 设为 argv
  `["${artifact}"]`。无强匹配时回退内置示例；再次执行 `init` 不得修改这两种
  manifest。并发创建者必须保留，不能被 `init` 替换；发布必须是原子的 no-replace。
- 在一个已发现 service 中人工添加端口、环境变量、依赖和 YAML 注释，再增加另一个
  强匹配一级子仓库并执行 `conven services --registry`。新路径必须加入，原 service block
  及其注释必须保持不变。从 workspace 子目录执行 `services --registry` 时必须扫描
  同一个 workspace 根目录。
- 把一个先前发现的仓库移出 workspace。普通 `services --registry` 保留其 service
  条目；`services --registry --prune` 从一级子目录发现范围中删除它。另用依赖 fixture
  验证：如果将被 prune 的 service 仍被引用，校验必须失败，manifest 应逐字节保持不变。
- 不满足任一强检测条件的仓库必须跳过。发现新 service 时不得虚构端口、环境变量、
  依赖路由、Apollo 行为或连接配置。
- 相邻仓库的 `go/main.go` package 声明尚未完成或没有 module directive 时，应报告为
  skipped，同时继续发现其他有效仓库；这些普通 WIP 状态不能中止扫描。
- Discovery 必须从同一份源 bytes 完成强类型合并和 YAML node 编辑，严格解码并校验
  最终编码的 YAML，再要求解码后的强类型 manifest 与已校验 candidate 完全一致。
  如果 YAML `<<` merge-key fixture 会从语义上恢复已 prune 的 service，必须拒绝且不
  修改 manifest。
- 紧邻 rename 前，discovery 必须执行文档约定的 best-effort same-file 和源 bytes
  检查。此处检测到冲突时，应保持当前 manifest 不变并提示重试。验收不能把它描述为
  针对任意外部 writer 的线性化 compare-and-swap；极窄的 check/rename 间隔仍是
  best-effort 限制。符号链接形式的 `.conven/conven.yaml` 也必须拒绝，不能替换链接本身。

上面的重定向 `services --logs --tail` 是非 TTY Plain 模式验收路径。它必须保持为
持续输出、带 `[service]` 前缀的日志流，并且不能输出 Dashboard 控制序列。服务自身
写出的 ANSI 字节可以保留在 Plain 日志流中；净化只用于全屏 Dashboard 渲染。前面的
start 刻意使用非 TTY 标准输入：启动成功后必须直接返回并让服务继续运行，不能进入
任何日志查看器。

还要在真实交互式终端中验收 Dashboard，不能使用重定向或管道：

```bash
"$CONVEN_BIN" services --start user-svc order-svc
# Start 默认打开 Dashboard；验收其控制键后按 q。
"$CONVEN_BIN" services --status
"$CONVEN_BIN" services --restart user-svc
# Restart 默认打开 Dashboard；验收其控制键后按 q。
"$CONVEN_BIN" services --dashboard user-svc order-svc
# 按 Ctrl-C；已重启服务和未变化服务都必须继续运行。
"$CONVEN_BIN" services --status
"$CONVEN_BIN" services --logs --tail --dashboard user-svc order-svc
# 最后一个模式参数选择 Dashboard 别名；验收后按 q。
"$CONVEN_BIN" services --logs --dashboard --tail user-svc order-svc
# 最后一个模式参数选择 Plain；使用原生 scrollback 和 Command+F，随后按 Ctrl-C。
"$CONVEN_BIN" services --stop --all
```

确认 `services --logs --tail`、`services --start --tail` 和
`services --restart --tail` 即使在 TTY 中也使用持续输出的 Plain 日志流；`--tail`
仍是布尔开关，不是日志行数选项。Plain 输出必须进入终端原生 scrollback，并可使用
Command+F 搜索。`services --logs --dashboard` 必须与独立的
`services --dashboard` action 行为完全一致。restart 也支持 `--dashboard`，并在可用
TTY 中默认打开 Dashboard。同时向 logs 或 restart 传入两个模式参数时，必须以最后
出现的参数为准：`--tail --dashboard` 打开 Dashboard，`--dashboard --tail` 选择 Plain。

全屏 Dashboard 必须用固定 banner 展示 workspace/environment、所选的运行中服务、
对应的 manifest 声明端口快照和当前 LAN IPv4。应用内历史最多保留最近 10,000 行
已接收日志。验收鼠标滚轮、`↑`/`↓` 和 `PgUp`/`PgDn` 滚动、`Home` 或 `g` 跳到最早保留日志、
`End` 或 `G` 返回实时跟随、`/` 搜索、`n`/`N` 切换匹配项，以及 `Esc` 清除搜索。
终端 Command+F 不是 Dashboard 历史搜索，只需看到当前渲染的一屏。调整终端窗口大小
必须触发布局重绘。使用一条超过终端宽度的 JSON 日志，确认它会折成可滚动的视觉行，
不会出现视口省略号或内容丢失。服务写出的 ANSI 或其他终端控制序列必须先净化。
顶部横线和字段/协议标签必须使用白色，仅本地服务数量数字使用绿色，居中的操作提示
使用黄色；Banner 不能使用背景色。
`q` 和 `Ctrl-C` 都只能脱离并恢复终端，不能停止服务，随后执行的 `services --status`
必须证明服务仍在运行。显示端口是配置快照，不是实时监听 socket 探测；LAN 地址与端口
同时显示，本身不能证明 endpoint 已绑定或可达。

确认以下行为：

- `services` 和 `doctor` 只从当前目录向上解析最近的 `.conven/conven.yaml`。根目录的
  `conven.yaml` 和 `.conven`
  中的其他文件都被忽略。嵌套 `.conven` 若没有 `conven.yaml`，必须作为不完整的硬边界
  报错，不能回退到有效的父级 workspace。仓库发现始终扫描解析出的 workspace 根目录
  的一级子目录，不能扫描命令调用目录的子目录。
- `services` 的动作参数必须位于其后的第一个参数，并且必须且只能指定 `--list`、
  `--registry`、`--status`、`--dashboard`、`--logs`、`--start`、`--restart`、`--stop`、
  `--stop-all`、`--cleanup` 之一。原顶层服务命令必须以用法错误状态 2 退出，并从 help
  和补全顶层候选中消失。
- 已移除的 workspace 和 manifest 参数在每个 workspace 命令中都必须以用法错误
  状态 2 退出，并且不出现在命令 help 或任何补全中。设置 `CONVEN_WORKSPACE` 不能
  改变 CLI 发现：位于 workspace 内时仍选择当前 workspace，位于 workspace 外时仍失败。
- workspace 外仍可使用 `init`、`config --global`、help、version、各命令的 help 和内部补全
  生成；本地 `config` 及服务命令必须给出清晰的初始化错误。不完整 `.conven`
  边界中的本地 `config` 在 `conven.yaml` 仍在准备时即可使用。
  用户级 `~/.conven` 始终专用于全局设置：即使其中被手工放入 manifest，也不得把
  HOME 或其子目录识别为 workspace；`conven init` 必须拒绝 HOME。
- 每个启动的本地服务都会在 `CONVEN_WORKSPACE` 中收到解析后的 workspace 绝对根目录，
  并覆盖继承值。它是只读的服务元数据，不作为 CLI 发现的输入。
- 本地配置保存在 `.conven/config`，全局配置保存在 `~/.conven/config`；本地
  `ktctl.path` 和 `ktctl.kubeconfig` 均优先于全局值。测试后分别使用
  `conven config --unset` 删除两个临时本地值。
- `services --start` 和 `doctor` 中，`--dev` 等效于 `--env dev`，`--test` 等效于
  `--env test`。检查 `--test` 前先在一次性 manifest 中添加 `test` profile。快捷参数
  与同值 `--env` 可组合；`--dev --test` 或快捷参数与不同值 `--env` 的组合必须在
  workspace 启动前失败。
- 每个 workspace 只有一个规范 manifest；环境差异由 `--env`、`--dev` 或 `--test`
  及匹配的 `environments` profile 表达。
- 只启动显式指定或在交互界面选中的服务。
- PathPicker 的候选项仍只来自 manifest，不能隐式触发仓库发现。
- 启动信息严格为 `Convening local services: user-svc, order-svc`。
- 对已选中的依赖注入 `localEnv`，对未选中的依赖注入 `remoteEnv`。
- 可通过 `conven services --logs` 查看所有选中服务的日志。`services --dashboard`
  及其 `services --logs --dashboard` 别名提供固定 banner 的 TTY 查看器和应用内最近
  10,000 行历史；`services --logs --tail` 提供使用原生 scrollback 的 Plain 日志流。
  logs 或 restart 同时出现两个模式参数时以最后一个为准。交互式 start/restart 默认
  打开 Dashboard，显式指定 `--tail` 会选择 Plain；非 TTY 缺省模式在执行完成后返回。
  Dashboard 中的长日志会折成可滚动的视觉行，同时保留原始可搜索的逻辑行。
- `services --status` 显示保存的 PID/PGID，普通 `services --stop` 会校验进程身份。
- PathPicker 使用 `f` 切换当前服务，使用 `A` 全选或清空；空选择仍停留在选择页。
  二次确认接受 `y`/`yes` 继续、`n`/`no` 取消，无效输入最多重新询问三次；非 TTY
  且未显式给出服务时安全失败。
- 循环依赖 fixture 按依赖分量启动，并在同一分量全部进程启动后执行健康检查。
- dry-run 不创建 session、不启动进程，也不建立连接。
- 在通用 fixture 中，把 `runner.workdir` 设为服务源码目录，并把支持模板的
  `runner.runWorkdir` 设到 `${runDir}/configs/${service}` 下，由 `prepare` 创建该目录。
  确认 prepare/build 在 `runner.workdir` 中执行，而服务进程和 `command` 类型健康检查
  在 `runner.runWorkdir` 中执行；还要验证相对 `runWorkdir` 基于 service 目录解析，
  省略时回退到 `runner.workdir`。
- 只把 `runner.runWorkdir` 改为另一个由 prepare 创建的目录，再执行无参数
  `services --restart`；计划 fingerprint 必须只选中并重启该服务。随后配置一个 prepare
  不会创建的 run workdir 并强制 restart；命令必须在停止现有服务进程前失败。
- 修改一个服务的源码或实际启动计划后，无 service 参数的 `services --restart` 只重启
  该服务；显式指定 service 时，即使没有变化也会强制重启。未变化服务、共享连接和
  session 日志路径必须保持不变。
- `services --stop-all` 与 `services --stop --all` 必须进入同一清理路径：停止当前完整
  session 并释放它的 connection lease；只有不存在其他活动 workspace lease 时才能
  终止 ownership 记录确认的 ktctl connection，其他 workspace 仍在租用的连接不能终止。
  对同时记录为 `Owned=false` 且 `Managed=false` 的外部 ktctl 进程或网络可达性，
  两种写法都只清理 session 引用，不能终止外部连接。
- 执行 `services --stop-all` 后，`services --cleanup` 只能删除
  `runtime/current/artifacts` 和 `runtime/current/logs`，必须保留运行配置和共享连接日志；
  仍存在 session、清理目标为 symlink 或路径越出 workspace runtime 时必须拒绝清理。

在支持 `ktctl` 的一次性环境中，还要验证 readiness、
kubeconfig/context/namespace 传递、共享连接租约、最后一个租约释放时停止连接，
以及经人工核对的孤儿连接恢复。设置 `KTCTL_KUBECONFIG`，确认它覆盖配置文件和
manifest 中的 kubeconfig 路径。使用 `connection.sudo: true` 时，确认先交互执行
`sudo -v`，再以 `sudo -n <resolved-ktctl> --kubeconfig <file> ... connect` 启动；
这也验证 sudo 不依赖自己的 `secure_path` 查找 ktctl。还要分别验收两种完整停止写法：
最后一个 lease 释放后 connection 必须终止；另一个 workspace 仍持有活动 lease 时必须
保持运行；同时为 `Owned=false` 且 `Managed=false` 的外部连接在 session 引用被清理后
也必须保持运行。使用 `--force` 前必须先确认保存的 PID/PGID。

稳定版本功能验收通过后，再保留一次 HEAD smoke test：

```bash
brew uninstall --formula "$CONVEN_FORMULA"
brew install --formula --HEAD "$CONVEN_FORMULA"
brew test --HEAD "$CONVEN_FORMULA"
"$(brew --prefix "$CONVEN_FORMULA")/bin/conven" --version
```

`brew reinstall` 不支持 `--HEAD`，所以发布测试机需要通过 uninstall/install 切换
channel。随后恢复并验证稳定版本：

```bash
brew uninstall --formula "$CONVEN_FORMULA"
brew install --formula "$CONVEN_FORMULA"
brew test "$CONVEN_FORMULA"
"$(brew --prefix "$CONVEN_FORMULA")/bin/conven" --version
```

## 8. 完成发布

- 确认准备好的源码 commit、Formula PR、每个 test-bot 平台和 `brew pr-pull`
  workflow 的 CI 都通过，并使用前述验收门禁确认最终 `origin/master` Formula
  和 bottle 安装。
- 在 release notes 中记录源码 tag、归档 URL 和 SHA-256、Formula PR，以及
  `conven-X.Y.Z` bottle Release。
- 确认中英文 README 的安装说明已经描述新发布的稳定 Formula。
- 保持 tag 不可变；任何修复都发布新的 patch 版本。
