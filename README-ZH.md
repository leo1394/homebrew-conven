# Conven

[English](README.md) | **简体中文**

`conven` 是面向本地微服务开发的聚焦启动工具。它只在本机启动当前改动涉及的
服务，其余依赖继续通过开发环境的注册中心访问，并把本次会话的服务日志集中到
`<workspace>/.conven/runtime/current`。

```text
Convening local services: user-svc, order-svc, payment-svc
```

Conven 本身使用 Go 编写，但被启动的服务不限语言。每个服务的准备、构建和启动
命令都由 workspace manifest 中的 argv 数组声明，因此可以运行 Go、Java、Node.js
或其他可由本地命令启动的服务。自动仓库发现的范围更窄：v1 通过可扩展的
`RepositoryAnalyzer` 只识别仓库根目录或 `go/` 子目录中的 Go main module；其他
布局仍可通过手工声明 manifest 完整支持。

## v1 能力边界

- 只启动显式指定或在交互界面选中的服务。
- 只重启当前会话中已变化或已退出的服务，同时保持未变化服务和现有集群连接运行。
- 对已选中的依赖注入 `dependencies.<service>.localEnv`，或按 policy 把对应 YAML
  binding 改为本地地址；对未选中的依赖注入 `remoteEnv`，或保留远程发现配置。
- 可从业务仓库或 Apollo 读取配置，在只读源码之外生成
  `.conven/runtime/current/configs/<service>`，再按 policy 叠加服务端口和依赖路由。
- 可通过 `ktctl connect` 建立“本机访问集群”的连接。
- 记录进程信息和每个服务的日志，支持后续查看和停止；健康检查只在启动阶段执行，
  不作为持续状态保存。
- 不提供 `ktctl exchange`、Mesh 或 Preview。集群流量不会反向路由到本地服务。
- 不自动推断业务依赖，也不把本地服务安全地注册到远程注册中心；这些行为必须
  在 manifest 中显式配置。
- 健康检查只能证明本地进程或端点可用。业务全链路仍需项目自己的 smoke test。

推荐在本地启动时通过 `localEnv` 关闭服务注册，避免其他开发环境流量进入个人
进程；未启动的依赖则通过 `remoteEnv` 保留远程发现配置。

## 环境要求

- macOS 或 Linux
- 从源码构建时需要 Go 1.23 或更高版本
- 通过 Formula 安装时需要 Homebrew
- 仅在运行 Python 插件时需要 Python 3
- 仅在使用 `ktctl` connection driver 时需要 `ktctl`
- 仅在 connection 设置 `connection.sudo: true` 时需要 `sudo`

## 安装

先添加一次 tap，再使用 Formula 短名称安装稳定版本：

```bash
brew tap leo1394/conven
brew install conven
```

如果要安装 `master` 上的开发版本，而不是最新稳定 tag，可执行：

```bash
brew install --HEAD conven
```

完成 tap 后，日常更新稳定版执行：

```bash
brew update
brew upgrade conven
```

如果要直接从 Conven 源码 checkout 构建，请在仓库根目录执行：

```bash
go build -o /tmp/conven ./cmd/conven
/tmp/conven --version
```

Formula 会安装 Bash、Zsh 和 Fish 补全。`__completion` 是 Formula 使用的内部命令，
通常不需要手工调用。

Conven 以 `.conven`、`conven.yaml`、`~/.conven`、`CONVEN_*` 和 `conven` 作为
workspace、用户目录、环境变量与命令的规范命名。不会再在 `.local/state` 或 XDG
状态目录下创建第二套用户状态根。

所有用户级文件统一位于一个根目录：

```text
~/.conven/
├── config
├── state/
│   └── connections/
└── plugins/
```

connection registry 与插件目录按需创建，并使用仅当前用户可访问的权限。每个项目的
运行文件仍隔离在各自的 `<workspace>/.conven/runtime` 中。

## 设计：通用能力 + 声明式项目规则

Conven 把可复用机制与具体项目约定分开：

- 通用能力负责只读仓库分析、服务计划、连接管理、repository/Apollo 配置输入、
  YAML 物化、进程生命周期和日志。
- 声明式项目规则集中保存在唯一的 `<workspace>/.conven/conven.yaml` 中。`policies`
  描述公司或技术栈约定，`services` 描述端口、依赖、binding 和 runner，
  `environments` 描述目标环境与连接。

Conven 不存在第二份 `.conven/policy.yaml`。公司约定、端口、字段名或本地/远程路由变化时，
通常只需修改声明；只有仓库布局、配置协议或物化语义无法由现有 analyzer、配置源和
materializer 表达时，才需要扩展代码。

## 快速开始

把当前目录初始化为 Conven workspace，检查从一级子目录识别出的仓库，再补充业务相关
的端口、环境变量和依赖路由：

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

上面的 service 名来自回退示例；如果识别到了仓库，应改为传入
`conven services --list` 输出的名称。

首次初始化时，`conven init` 只扫描当前 workspace 的一级子目录中的 Git 仓库。v1 内置
两个 `RepositoryAnalyzer`：`go-root-module` 检查仓库根目录的 `go.mod`/`main.go`，
`go-subdirectory-module` 检查 `go/go.mod`/`go/main.go`。两者都要求 `main.go` 声明
`package main`，且 module path 的最后一段与仓库目录名相同。命中项以仓库名作为
service 名和 `path`，生成相应的 `runner.workdir`、
`runner.build: [go, build, -o, "${artifact}", .]` 和
`runner.run: ["${artifact}"]`。Analyzer 还会保守识别 go-zero 的 HTTP/RPC server kind，
并从显式 YAML tag 提取 RPC client binding 候选；无法唯一判断时保持未知，不猜测。
`RepositoryAnalyzer` 当前是代码级扩展点，v1 不会按 manifest 中的名称动态加载第三方
analyzer。

如果 WIP 仓库的对应 `main.go` 暂时无法提供有效的 `package main` 声明，或 `go.mod`
尚无 module directive，该仓库会被报告为 skipped，但不会阻断其他仓库的发现。

如果没有仓库强匹配，`init` 回退到构建时从
[`examples/application.yaml`](examples/application.yaml) 嵌入的模板。重复执行
`init` 是安全的：已有 manifest 只会被报告，不会覆盖。首次发布采用原子的
no-replace 操作，因此并发创建的 manifest 会保留而不会被覆盖。一级子仓库集合变化
后使用 `conven services --registry`。首次使用运行目录前，Conven 会在不覆盖已有规则的
前提下把 `/runtime/` 加入 `.conven/.gitignore`。

初始化严格按以下顺序执行：

1. 解析 workspace 目录并检查 `.conven` 边界。
2. 只读扫描一级子 Git 仓库。
3. 对各仓库运行内置 `RepositoryAnalyzer`。
4. 只记录 analyzer 能证明的 `path`、runner、kind 与 discovery/binding 候选。
5. 以原子 no-replace 方式创建 `.conven/conven.yaml`；只有 `init` 在零强匹配时可使用
   embedded example。
6. 把缺失的通用内置插件安装到 `~/.conven/plugins`，已有用户副本绝不覆盖；当前内置
   插件集合为空。
7. 不访问 Apollo、不创建 runtime、不构建或启动服务，也不向子业务仓库写文件。
8. 通过 `conven policy --edit` 补充扫描无法推断的项目规则，或用
   `conven policy --import <yaml-file> --edit` 导入项目本地生成器输出的完整候选。
   然后执行 doctor、start dry-run 和实际 start。

重复执行 `init` 不覆盖规范 manifest，也不是恢复操作。

`services --start` 默认在后台启动服务。需要立即进入实时日志 Dashboard 时，使用布尔开关
`--tail`：

```bash
conven services --start --tail user-svc order-svc
```

先检查计划而不启动进程：

```bash
conven services --start --dry-run user-svc order-svc
```

## 编辑、导入或重置项目规则

`policy` 是命令名称，不代表另有一份 policy 文件。以下三个主动作都针对完整的唯一规范
文件 `<workspace>/.conven/conven.yaml`：

```bash
conven policy --edit
conven policy --import ./generated-conven.yaml
conven policy --import ./generated-conven.yaml --edit
conven policy --reset
```

`--edit` 先创建当前 manifest 的私有临时草稿，再依次选择 `CONVEN_EDITOR`、`VISUAL`、
`EDITOR`，均未配置时使用 `vi`。编辑器命令可以带参数，例如
`CONVEN_EDITOR="code --wait"`；图形编辑器必须等待文件关闭。只有编辑器正常退出、草稿
通过严格 schema 和语义校验、且发布前的 best-effort 检查未发现规范源文件 snapshot
变化时，Conven 才按草稿原始 bytes 原子发布。编辑器失败、YAML 无效、存在未知字段或
检测到并发编辑时，Conven 不发布该草稿，也不覆盖检测到的并发版本。被拒绝且已变化的
草稿会以 `0600` 保存在 `.conven/backups/`，Conven 同时把 `/backups/` 非破坏性加入
`.conven/.gitignore`。

提交阶段会对当前 manifest inode 获取非阻塞 advisory lock，并在 rename 前再次核对
same-file 身份和源 bytes，从而串行化所有配合该协议的 Conven writer。任意外部工具不必
遵守该锁，因此与外部 writer 的最后 check→rename 窄窗口仍属于 best-effort 冲突检测。

`--import <yaml-file>` 读取一份完整的本地 Conven v1 manifest，并把它的原始 bytes 作为
整个 `.conven/conven.yaml` 发布。相对源路径从命令调用时的 cwd 解析，不以 workspace 根目录
为基准。导入不会编辑或移动源文件，也不会把候选与原有扫描字段或人工字段 merge。
目标发生变化时，原 manifest 会先以 `0600` 备份到 `.conven/backups/`；内容逐字节相同
时不替换。增加 `--edit` 后，Conven 以导入内容创建私有草稿并打开编辑器，源文件仍保持
不变；因此也可以先在草稿中修复原本无效的候选。无论是否编辑，只有最终候选通过严格
schema、语义校验和既有并发检查后才会发布。

导入通过校验只说明它是一份合法的 Conven v1 manifest，不代表端口、依赖路由、
Apollo/Consul endpoint、凭据、源码路径或服务命令在本机可运行。导入后必须先执行
`conven doctor` 和 `conven services --start --dry-run SERVICE...`，再实际启动。

`--reset` 是显式的破坏性重置操作：它根据当前一级子 Git 仓库的只读分析
完整重建 manifest，不是回滚、merge 或 `services --registry` 的别名。替换现有
manifest 前，Conven 会把原始 bytes 以 `0600` 保存到 `.conven/backups/` 并输出备份路径。
只要真实 `.conven` 边界仍存在，它可以重建缺失或内容损坏的 manifest。若扫描不到任何
受支持仓库，则失败且不发布替换文件，也绝不回退 embedded example。

> **警告：** 扫描重置无法保留或还原 `workspace.policy`、`policies`、`environments`、
> 端口、依赖拓扑或 binding 分配、`env`、健康检查、service patch、人工 runner 修改及
> YAML 注释。Analyzer 给出的 binding 只是候选，不是被还原的依赖图。

重置为扫描基线后，必须重新声明并验证项目规则：

```bash
conven policy --reset
conven policy --edit
conven doctor
conven services --start --dry-run SERVICE...
```

| 命令 | 用途 | 人工规则处理方式 |
| --- | --- | --- |
| `conven init` | 创建缺失的 manifest | 已存在时绝不覆盖 |
| `conven policy --edit` | 编辑并校验临时草稿 | 保留用户未修改的内容 |
| `conven policy --import <yaml-file> [--edit]` | 发布完整本地 v1 manifest，可先编辑私有草稿 | 整体替换；备份旧文件，不 merge，也不修改源文件 |
| `conven services --registry` | 保守合并新的扫描事实 | 人工非空字段优先 |
| `conven policy --reset` | 从扫描事实重建整个 manifest | 丢弃人工声明；从输出的备份恢复或重新编辑 |

### 哪些字段可生成，哪些必须确认

当前 v1 只有中央 `.conven/conven.yaml`；Conven 不创建或读取分散的 `.conven/service.yaml` 或
独立 Policy Profile 文件。所谓“项目提交标准配置”，是把 service、policy 和 environment
profile 一起提交在中央 manifest 中。各来源的边界如下：

| 字段 | 仓库扫描可生成 | 项目本地生成器 | 仍需人工确认或填写 |
| --- | --- | --- | --- |
| `version`、`workspace.name` | `version: 1`、目录 basename | 可应用项目默认值 | 候选是否用于正确 workspace |
| service name/path | 一级子 Git 仓库 | 可限制已审阅的服务清单 | 非标准布局、重命名或缺失服务 |
| `kind`、`discovery` | AST 唯一可证的 HTTP/RPC kind、analyzer、binding 候选 | 可解析已知项目约定 | 模糊 kind；binding 候选对应哪个真实依赖 |
| runner | 标准 Go root 或 `go/` module 的 workdir/build/run | 可应用已审阅的项目 runner | 特殊 argv、prepare、artifact、`runWorkdir` |
| policy/driver/config/routing | 不生成 | 可编码已审阅的框架和路由默认值 | bootstrap 字段、注册关闭和路由语义 |
| environment/connection | 不生成 | 可输出不含凭据的连接骨架 | cluster、namespace、context、网络入口和认证 |
| ports | 不生成 | 可应用已审阅的项目端口表 | 实际监听端口与本机冲突 |
| dependencies | 只产生 binding 名候选，不生成依赖图 | 可映射已审阅的项目依赖 | 完整业务依赖、target service/port、哪些保留远程发现 |
| patches/health | 不生成 | 可应用已审阅的项目默认值 | 业务副作用、协议健康检查与 smoke test |
| kubeconfig/密钥 | 不生成 | 不应硬编码 | 用 `conven config`、环境变量或外部凭据系统配置 |

仓库 analyzer 与项目专用生成器的权限边界不同。Analyzer 只输出从源码中能够证明的
path、标准 runner、kind 和 binding 候选。生成器可以把这些扫描事实与项目明确约定的
端口、dependency target、policy driver、environment profile、patch 和 health check
组合起来，但这些默认值仍是需要人工审阅的 policy，不会因为由脚本写出就变成扫描
事实。因此生成结果是一份完整候选，不是局部 overlay。

这类生成器的“一次审阅”流程是：

```bash
./generate-project-conven-policy              # 生成完整本地 v1 候选
conven policy --import ./generated-conven.yaml --edit
conven doctor
conven services --start --dry-run SERVICE...
```

审阅后应提交规范的 `.conven/conven.yaml`，而不是宣称所有字段都可自动推断。只有仓库结构
或项目默认规则变化时，才重新执行生成/import 流程。

导入不做字段级合并，而是：本地完整候选 → 可选 `--edit` 修改 → 严格校验后
整体发布。如需把 analyzer 元数据补回候选中的空字段，发布后显式执行
`conven services --registry`；该命令仍只保守补空，不会推导端口或依赖图。

当团队已经审阅并提交标准 `.conven/conven.yaml` 后，“人工确认一次”由项目维护者完成。
普通开发者不应在每次 clone 后再次导入生成候选，而是只配置
本机 kubeconfig/凭据，运行 `conven doctor` 和 start；仓库结构或项目规则变化时再由维护者
重新生成/import，或执行 registry/edit 并提交。

## 更新已发现的服务

可以在 workspace 根目录或任意子目录执行：

```bash
conven services --registry
conven services --registry --prune
```

`services --registry` 从当前目录解析最近的 workspace，但始终扫描该 workspace 根目录的
一级子 Git 仓库，并复用 `init` 的 `RepositoryAnalyzer`。新匹配的仓库路径会加入
`services`；如果某个 service 已关联同一路径，Conven 只会补齐缺失的 `kind` 或整段
`discovery` 元数据，人工填写的非空字段、runner 和 YAML 注释优先且不会被覆盖。
默认保留本次扫描中缺失的条目。

`--prune` 会同步一级子目录发现范围中的缺失路径，删除对应仓库已不存在或已不再是
目录的条目；仍存在但不受支持的仓库不会被 prune。写入前会校验合并后的完整
manifest；如果保留的 service 仍依赖将被删除的 service，prune 会失败且不修改
manifest，应先调整依赖再重试。manifest 不记录条目来自生成还是手写，因此人工管理
的 service 如果也指向一级子目录，使用 `--prune` 前必须审阅结果。

执行更新时，`services --registry` 从同一份源 bytes snapshot 解码强类型 manifest 和
待编辑的 YAML tree。发布前还会对最终 YAML 再次执行严格解码与校验，并要求解码后的
强类型 manifest 与已经校验的 candidate 完全一致。这能防止 YAML `<<` merge key 在
显式条目已被删除后，又从语义上恢复被 prune 的 service。

紧邻 rename 前，`services --registry` 会以 best-effort 方式重新检查路径仍指向
same-file，且内容仍等于源 bytes snapshot。已检测到冲突时会中止发布并提示重试；
这不是针对任意外部 writer 的线性化 compare-and-swap，检查与 rename 之间极窄窗口内
的编辑仍可能被替换。该动作明确拒绝符号链接形式的 `.conven/conven.yaml`，因为原子替换
会破坏链接本身，而不是更新其目标。

发现功能刻意不推断端口、完整依赖图、`env`、Apollo 凭据、公司 policy 或集群连接
设置。仓库被发现只表示 Conven 能推导运行入口及少量静态描述信息，并不证明该服务
无需进一步编写 manifest 和执行项目 smoke test 就能加入端到端开发链路。

## PathPicker 交互

`conven services --start` 没有指定 service 时，在 TTY 中打开内置 PathPicker。候选项只来自
manifest 的 `services`；PathPicker 本身不会扫描或猜测仓库。仓库扫描只发生在
`init` 或显式执行的 `services --registry` 中。

| 按键 | 动作 |
| --- | --- |
| `j` / `k`、`↓` / `↑` | 移动光标 |
| `f` | 选中当前服务；再次按 `f` 取消 |
| `F` | 切换当前服务，然后移动到下一项 |
| `a` | 在全选和清空之间切换 |
| `Enter` | 已有选择时进入确认页 |
| `q` / `Esc` / `Ctrl-C` | 取消 |

确认页会完整显示：

```text
Convening local services: user-svc, order-svc, payment-svc
```

只有输入 `y` 或 `yes`（大小写不敏感）并按 `Enter` 才会启动。其他输入会取消。
没有选中任何服务时，`Enter` 不会越过选择页。非 TTY 或没有候选服务时会返回错误；
读取输入失败时也会返回错误；用户主动取消时正常退出。以上情况都不会隐式启动任何
服务。

## 重启变化的服务

`conven services --restart` 不传 service 时，会检查当前成功会话中选中的每个服务。
只有服务进程已经退出、源码目录 fingerprint 发生变化，或解析后的运行计划
fingerprint 发生变化时才会重启；比较基准是该服务最近一次成功 start 或 restart 时
记录的 fingerprint。
在 Git worktree 中，源码 fingerprint 覆盖 service 目录下的 tracked 文件和未被忽略的
untracked 文件；非 Git 目录则覆盖除 `.git`、`.conven` 外的目录内容。计划 fingerprint
覆盖解析后的 prepare/build workdir、run workdir、artifact、声明端口、命令 argv、
环境变量、健康检查配置，以及解析后的 policy/config materialization 计划。因此只修改
`runner.runWorkdir` 或 policy 路由也足以让无参数
`services --restart` 选中该服务。
远端 Apollo 内容本身不属于本地 fingerprint；如果只有远端配置发生变化，应显式传入
service 名强制 restart，才能重新获取并物化该服务配置。

显式传入 service 名可强制重启当前会话中的这些服务，即使 fingerprint 没有变化：

```bash
conven services --restart
conven services --restart user-svc order-svc
conven services --restart --tail user-svc
```

Restart 会复用当前会话的 environment、`.conven/runtime/current`、连接和各 service
的日志路径，不会重新建立连接，也不会中断未变化的服务。它只为目标服务重新物化
配置、执行 prepare/build，并在原日志追加新输出前写入 restart 标记；未变化服务的
artifact、config 和日志保持不动。所有目标的配置物化、prepare 和 build 完成后，Conven
还会验证每个目标解析后的 run workdir；目录缺失或不是目录时，restart 会在旧进程仍
运行的情况下中止。Fingerprint
会在这些步骤前捕获，并且只在成功启动后提交，因此 build 期间产生的新编辑仍会在
下一次 `services --restart` 中被识别。

## Tail Dashboard

`--tail` 是 `services --start`、`services --restart` 和 `services --logs` 都支持的
布尔开关，不接收日志行数参数。
在至少为 20 列、4 行的可用交互式 TTY 中，服务启动或重启完成后，它会打开全屏
Dashboard。固定 banner 显示 workspace、environment、当前 LAN IPv4，以及每个已启动
服务和从 manifest 快照保存的具名端口值；剩余区域聚合每个已选日志最近 80 行，并在
新输出到达时持续滚动。

按 `q` 或 `Ctrl-C` 离开 Dashboard。该操作只脱离日志视图，不会停止后台运行的
本地服务。终端窗口 resize 后 Dashboard 会重绘；服务输出中的 ANSI 及其他终端
控制序列会在渲染前被净化。

输入或输出任一不是 TTY 时（包括重定向或管道），或者 `TERM=dumb`、无法读取终端
尺寸、终端小于 20x4 时，`--tail` 会降级为持续输出的普通文本流。每行带
`[service]` 前缀，并且不会输出 Dashboard 控制序列。

显示的端口是服务启动或重启时从 manifest 声明中保存的配置快照，不是对当前监听
socket 的实时探测。banner 同时出现 LAN 地址和声明端口，并不保证进程绑定到该
interface，也不保证该 endpoint 实际可达。

## CLI

```text
conven init
conven config [--global] [--list|--unset] [key] [value]
conven policy --edit
conven policy --import <yaml-file> [--edit]
conven policy --reset
conven plugins --install <python-file>
conven plugins --list
conven plugins --run <name> [plugin args...]
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

`policy` 必须先指定且只能指定一个主动作；`--import` 后的 `--edit`
是该动作的可选修饰符，不是第二个主动作。Import 必须且只能接受一个源路径，其他动作
不接受位置参数。它们只要求解析到最近的真实 `.conven` 边界，不要求旧 manifest 先通过
解析，因此 edit 可修复无效内容，import 或扫描重置也能重建缺失文件。候选校验
失败、符号链接或检测到的发布冲突都不会发布替换内容。

动作参数必须是 `services` 后的第一个参数，并且必须且只能指定 `--list`、`--registry`、
`--status`、`--logs`、`--start`、`--restart`、`--stop`、`--stop-all` 之一。原顶层
`list`、`discover`、`status`、`logs`、`start`、`restart`、`stop` 命令已破坏性移除，
调用时返回用法错误。

`services --start` 支持：

```text
--env NAME             默认 dev
--dev                  等效于 --env dev
--test                 等效于 --env test
--kubeconfig FILE
--context NAME
--namespace NAME
--tail
--dry-run
--skip-build           仅跳过 build；全新 start 的默认 artifact 不可复用
--skip-verify
```

`services --start` 和 `doctor` 都支持 `--dev`、`--test`。对应 profile 仍须在 manifest 的
`environments` 中声明。快捷参数可以和相同值的 `--env` 同时使用；快捷参数互相冲突
或与不同的 `--env` 同时出现时，会在 workspace 启动前报错。

`services --restart` 支持：

```text
--tail
--skip-build           跳过 build，并复用 runtime/current 中的 artifact
--skip-verify
```

`services --restart` 刻意不提供 `--env`、`--kubeconfig`、`--context`、
`--namespace`，因为它会复用当前会话和连接。

每次 `services --start` 都是全新启动：确认没有活动或身份不可信的已保存进程后，
Conven 会安全重建 `.conven/runtime/current/{artifacts,configs,logs}`。因此
`services --start --skip-build` 不会复用默认 `${artifact}`。如果服务声明了非空
`runner.build`，且 `runner.run` 引用了该默认 artifact，Conven 会在
建立连接或启动进程前直接报错。需要在 `services --start` 时复用构建产物，请把
`runner.artifact` 显式设为 workspace 中的持久路径，例如 `${serviceDir}/bin/service`，
并确保该文件已经存在。`services --restart` 不会重建 `current`，因此
`services --restart --skip-build` 可以复用其中已有的默认 artifact。

`services --start --dry-run` 只读取本地 manifest、源码和静态配置来构建并校验计划；
不会创建、重建或修改运行目录，不请求 Apollo、不建立网络连接，也不执行
materialize、prepare、build 或服务命令。start 失败并完成回滚后，
会保留不完整的 `current` 供排查，下次安全的全新 start 再清理。
`services --stop` 永远不会删除 `current`；停止会话中的全部服务后，Conven 会清理
session、释放 connection lease，并继续保留 artifact、config 和日志，直到下次全新
start。

`services --stop` 可以接收服务名，也可用 `--all` 停止当前 workspace 会话中的全部
服务。
如果进程 leader 已退出、身份不再匹配但保存的进程组仍存活，普通停止会保留状态并
拒绝误杀。`conven services --status` 会显示保存的 PID/PGID；确认 PGID 属于本次 Conven
会话后，可对指定服务使用 `conven services --stop --force SERVICE`，或使用
`conven services --stop --all --force` 清理整个会话。`--force` 会绕过身份校验并直接向
保存的 PGID 发信号，只应用于人工确认过的恢复场景。

`conven services --stop-all` 是 `conven services --stop --all` 的严格简写；两者进入同一条
服务清理和连接释放路径。它们只释放当前 workspace 的 connection lease，并且仅在已
没有任何活动 workspace lease 时终止由 Conven ownership 记录确认的 ktctl connection；
仍被其他 workspace 租用的连接不会被终止。
同时记录为 `Owned=false` 且 `Managed=false` 的外部 ktctl 进程或网络可达性不归 Conven
所有；两种写法都只清理当前 session 引用，绝不会终止该外部连接。

当 workspace 已无 session 时，`conven services --status` 会列出
`~/.conven/state/connections` 中的共享连接 fingerprint、PID/PGID 和有效租约数。确认目标后，
`conven services --stop --all --force` 会检查这些记录，并只强制清理没有活动 workspace
租约的连接。其他 workspace 的活动租约会保留；普通陈旧租约在固定 5 分钟宽限期后
回收。
该路径用于 Conven 异常退出后恢复遗留的连接进程组和记录。该恢复命令仍须从能发现
有效 manifest 的 workspace 中执行。
`services --logs` 可以接收服务名，并支持上文说明的布尔开关 `--tail`。`doctor` 接受
`--env`、`--dev`、`--test`、`--kubeconfig`、`--context` 和 `--namespace`。

完整参数以对应命令的 `--help` 输出为准。

## Git 风格配置

Conven 把用户级设置保存在 `~/.conven/config`，workspace 设置保存在 `.conven/config`。
不带 `--global` 时，读取和 `--list` 显示“全局+本地”合并后的有效配置，本地值覆盖
全局值；写入和 `--unset` 只修改本地文件。带 `--global` 时，所有操作都只针对用户级
文件。

```bash
# 显示本地覆盖全局后的有效值；要求当前目录处于 .conven workspace 边界内。
conven config --list
conven config ktctl.path
conven config ktctl.path /opt/homebrew/bin/ktctl
conven config ktctl.kubeconfig /secure/dev-kubeconfig
conven config --unset ktctl.path
conven config --unset ktctl.kubeconfig

# 用户级 scope；在 workspace 外也可使用。
conven config --global --list
conven config --global ktctl.path '~/bin/ktctl'
conven config --global ktctl.kubeconfig '~/.kube/dev-config'
conven config --global --unset ktctl.path
conven config --global --unset ktctl.kubeconfig
```

删除本地 key 后，同名全局值会重新生效。两个文件都是扁平 YAML map，并使用仅当前
用户可访问的权限创建。Conven 实际消费的 ktctl runtime 设置是 `ktctl.path` 和
`ktctl.kubeconfig`。`ktctl.path` 可以是绝对路径、以 `~/` 开头的路径，或通过 `PATH`
解析的命令名；相对的 `ktctl.kubeconfig` 从 workspace 解析，也支持绝对路径和 `~/`。

## Python 插件

Conven 保留随版本嵌入通用插件的能力：`conven init` 会安装缺失的内置插件，同时绝不
覆盖已有用户副本。当前内置插件集合为空，并且 Conven 不打包公司或项目专用插件；
这类插件需要从本地 Python 文件显式安装，相对源路径按命令执行时的当前目录解析：

```bash
conven plugins --install ./generate-apollo-consul.py
conven plugins --list
```

安装时按源文件 basename 复制到 `~/.conven/plugins`，并把安装副本设为仅当前用户可执行
的 `0700`。源文件必须是普通、非符号链接、带 Python 3 shebang 的 `.py` 文件。目标已
存在时绝不覆盖；需要升级时，应先审阅并显式删除旧文件，再重新安装。

在已初始化 workspace 的任意子目录中，可使用不带 `.py` 后缀的文件名运行插件。
插件名后的全部参数都会原样透传，但 `--workspace` 是 Conven 保留参数：

```bash
conven plugins --run generate-apollo-consul
conven plugins --run generate-apollo-consul --output candidate.yaml
conven plugins --run generate-apollo-consul --check
```

Conven 以 workspace 作为脚本 cwd，在参数前添加
`--workspace <workspace绝对路径>`，同时设置
`CONVEN_WORKSPACE=<workspace绝对路径>`，并转发 stdin、stdout、stderr 和终端。调用方
传入的 `--workspace` 会被拒绝，不能替换已解析的 workspace。例如，独立维护的
Apollo/Consul generator 可以在当前 workspace 中写出
`<workspace-name>-apollo-consul.yaml` 候选文件。Conven 不会自动发布候选；应先审阅，
再执行 `conven policy --import <file> --edit`。

用户可以把自己的可执行 `*.py` 文件放入 `~/.conven/plugins`。Conven 只列出和运行
普通、可执行、非符号链接的文件，并拒绝可能越出插件目录的名称。重复执行
`conven init` 只补齐缺失的通用内置插件，所有已存在的插件文件都会保留。

## Workspace 边界与 Manifest 查找

唯一识别的 manifest 是 `<workspace>/.conven/conven.yaml`；workspace 根目录的
`conven.yaml` 和 `.conven` 中的其他文件都不作为 manifest。Conven 从当前目录向上
查找，并在遇到最近的 `.conven` 目录时停止。该目录是硬 workspace 边界：
如果其中没有 `conven.yaml`，Conven 会报告 workspace 不完整，不会继续使用
父级 workspace。如果没有找到 `.conven` 目录，则当前目录不属于 Conven workspace。
用户级 `~/.conven` 目录专用于全局设置，永远不构成 workspace 边界。因此
`conven init` 会拒绝用户 HOME；应改在项目目录中初始化。

CLI 和环境变量都不能覆盖 workspace 发现结果。需要对其他 workspace 执行命令时，
由调用方的 shell 或脚本切换目录：

```bash
(cd /path/to/workspace && conven services --status)
```

每个 workspace 只有一个规范 manifest。环境差异通过 `--env`、`--dev` 或 `--test`
及对应的 `environments` profile 表达，不通过选择其他 manifest 表达。
这份 `.conven/conven.yaml` 也是 workspace 的集中式自描述：它统一保存仓库路径、静态分析
结果、runner、端口、依赖关系、环境连接和可复用 policy。仓库发现产生的 service
声明只更新这一个文件；`conven init` 还可能在中央 `.conven/.gitignore` 中非破坏性加入
`/runtime/`，但不会向任何子业务仓库写入 Conven 配置或运行时副本。

`conven policy` 操作的也是这一个规范文件；Conven 不创建或读取 `.conven/policy.yaml`。
该命令要求最近的真实 `.conven` 边界，但旧 `conven.yaml` 内容无效时仍可打开、从本地
候选或模板替换，或按扫描重建；边界存在时也可重新创建缺失的 manifest。四个 policy
主动作都拒绝符号链接形式的边界或 manifest。

运行类 workspace 命令（`services` 和 `doctor`）要求该边界和有效的已解析 manifest。
在 workspace 外，只有
`help`、`--help`、`--version`、`init`、`config --global` 和内部补全生成可实际
运行；各子命令的 `--help` 也可以在任意目录查看。不带 `--global` 的 `config` 使用
最近的 `.conven` 硬边界，并且在 manifest 仍在准备时即可使用。

Conven 会把解析后的 workspace 绝对根目录作为 `CONVEN_WORKSPACE` 注入每个本地服务，
并覆盖继承的同名值。该变量是供服务进程读取的只读元数据；Conven CLI 不会读取
它来发现或选择 workspace。

v1 manifest 必须满足：

- `version` 为 `1`。
- `workspace.name` 非空。
- 至少声明一个 service。
- 每个 service 都有 `path` 和非空的 `runner.run` argv。
- service 名以字母或数字开头，且只包含字母、数字、`.`、`_`、`-`。
- `prepare`、`build`、`run` 中不能包含空 argv 项。
- 端口必须在有效范围内，依赖只能引用同一 manifest 中的其他服务。

最小示例：

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

主要字段及多语言 runner 示例见
[`examples/application.yaml`](examples/application.yaml)。命令使用 argv 数组而不是
shell 字符串，因此 `&&`、管道和重定向不会被 shell 隐式解释；如确实需要 shell
行为，应把 `sh -c` 作为显式 argv 写入，并自行处理转义风险。

### 关键字段

| 字段 | 用途 |
| --- | --- |
| `workspace.name` | workspace 的稳定名称 |
| `workspace.policy` / `services.<name>.policy` | workspace 默认 policy，以及可选的 service 覆盖 |
| `policies.<name>.drivers` | framework、配置来源、服务发现和 materializer 的选择 |
| `policies.<name>.config` | 配置源目录、application/bootstrap、Apollo 重试及公共 YAML patches |
| `policies.<name>.process` | policy 统一追加的进程环境变量和 argv |
| `policies.<name>.routing` | 按 service kind 修补监听端口，并定义本地/远程依赖 YAML 路由 |
| `environments.<name>.env` | 当前环境下所有本地服务共享的环境变量 |
| `environments.<name>.registry` | 注册中心类型的说明字段，v1 不会自动解释 |
| `environments.<name>.connection` | `none`、`ktctl` 或 `command` 连接配置 |
| `environments.<name>.connection.command` | `ktctl` executable 的可选 fallback，或 `command` driver 必填的 executable |
| `environments.<name>.connection.sudo` | 可选；通过 `sudo` 启停连接进程 |
| `services.<name>.path` | 相对 workspace 或绝对的服务目录 |
| `services.<name>.kind` / `discovery` | 服务类型，以及 analyzer 与静态提取的 binding 候选 |
| `runner.workdir` | 相对服务目录或绝对的 prepare/build 工作目录 |
| `runner.runWorkdir` | 可选的 run 和 command health 工作目录；支持模板，默认使用 `runner.workdir` |
| `runner.prepare/build/run` | 依次执行的 argv；`run` 必填 |
| `runner.artifact` | 可选的构建产物路径 |
| `ports` | 供模板引用的端口名称到数字映射 |
| `env` / `localEnv` | 服务公共变量及本地启动变量 |
| `dependencies` | 用 `binding`/`port` 生成 YAML 路由，或按选择注入 `localEnv`/`remoteEnv` |
| `health` | `process`、`tcp`、`http` 或 `command` 健康检查 |

### Policy、Driver 与配置物化

Policy 用来把同一公司或技术栈的约定集中声明一次。service 默认继承
`workspace.policy`，也可用自己的 `policy` 覆盖。当前 driver 的职责边界如下：

Policy 定义、service 选择和环境声明始终位于同一 manifest。使用
`conven policy --edit` 执行受校验的人工修改，使用 `services --registry` 保守刷新仓库
扫描事实。`policy --import <yaml-file> [--edit]` 可整体采用本地完整候选且不修改源文件，
`policy --reset` 只能重建扫描事实，不能重建 policy。

| Driver | 当前作用 |
| --- | --- |
| `framework`、`discovery` | 记录 policy 的框架和发现机制分类，供计划和诊断展示；本身不启动框架或注册中心 |
| `configSource: repository` | 从 service 仓库内、由 policy `config.sourceDir` 指定的目录读取 YAML |
| `configSource: apollo` | 从 bootstrap 读取 Apollo 连接信息并获取 application 内容 |
| `materializer: yaml-overlay` | 复制到 staging，应用 policy/server/service/dependency patches，校验后原子发布 |

物化结果固定写入：

```text
<workspace>/.conven/runtime/current/configs/<service>/
```

业务仓库中的 `resources/application.yaml`、bootstrap 和其他源文件始终只读。对于
`repository`，application 从仓库副本开始叠加；对于 `apollo`，application 内容由
远端配置替换，bootstrap 可另存为 runtime bootstrap。Conven 先应用公共 policy patch，
再应用按 kind 的 server patch 和 `services.<name>.config.patches`，最后根据本次选择
应用依赖路由。`services --registry` 不覆盖人工填写的非空 manifest 字段；service patch
也可以覆盖公共/server 默认值，而最后的 dependency route 专门保证本次本地/远程选择。

Materializer 只把 policy `config.sourceDir` 的内容复制到 `configs/<service>`，并不自动
构造脚本式的完整 `go/ + resources/` 运行目录。Policy 可通过
`-f ${configDir}` 使用物化后的 application/bootstrap；如未声明
`runner.runWorkdir`，进程 cwd 仍是源码 workdir，其他类似
`../resources/...` 的相对路径仍会只读访问源码资源。若服务要求完整独立
运行布局，应显式声明 `runner.runWorkdir` 及相应 prepare/layout 规则。

完整的公司/项目声明应保存在项目自身的受控仓库中。在那里生成不含凭据的
候选，通过 `conven policy --import <yaml-file> --edit` 审阅，再提交规范
`.conven/conven.yaml`。本机 kubeconfig 路径可用 `conven config ktctl.kubeconfig <path>`
配置；凭据应留在环境变量或外部凭据系统中。

### 独立运行目录

当 prepare 或 build 需要在源码目录执行、而服务必须从生成的运行时资源目录启动时，
使用 `runner.runWorkdir`：

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

`prepare` 和 `build` 仍在 `runner.workdir` 中执行；服务进程和 `command` 类型健康检查
在 `runner.runWorkdir` 中执行。绝对路径直接使用；相对路径基于 service 目录解析，
而不是基于 `runner.workdir`。省略时默认使用解析后的 `runner.workdir`。

声明 `prepare` 时，规划 manifest 阶段 run workdir 可以尚不存在，并由 `prepare`
创建；没有 `prepare` 时，该目录必须已经存在。Conven 会在 prepare/build 完成后、
启动进程前确认它是目录；restart 会先验证所有目标，再停止任何旧目标进程。

建议把生成的 run workdir 放在 `${runDir}` 下；该变量解析为
`<workspace>/.conven/runtime/current`。如果它位于 service 目录内，应确保
生成文件已被版本控制忽略，否则 source fingerprint 可能让下一次无参数
`services --restart` 再次选中该服务。

本地依赖图允许循环依赖。Conven 会先把循环中的服务归为同一个强连通分量，再按分量
之间的依赖顺序启动；同一分量内按 service 名稳定排序，并在全部进程启动后统一执行
健康检查。

`runner.runWorkdir`、runner argv、环境变量值、健康检查地址/命令，以及 connection 的
command/args/kubeconfig/context/namespace/readiness 地址可使用以下模板：

```text
${workspace}  ${service}  ${serviceDir}  ${stateDir}
${runDir}     ${artifact} ${env}
${port.NAME}
${services.SERVICE.ports.NAME}
```

workspace 运行时模板和注入的环境变量具有以下固定含义：

| 模板 / 变量 | 解析路径 |
| --- | --- |
| `${stateDir}` / `CONVEN_STATE_DIR` | `<workspace>/.conven/runtime` |
| `${runDir}` / `CONVEN_RUN_DIR` | `<workspace>/.conven/runtime/current` |
| `${artifact}` / `CONVEN_ARTIFACT` | 默认是 `current/artifacts/<service>` |
| `CONVEN_CONFIG_DIR` | `current/configs/<service>` |

`ktctl` driver 会自动在命令末尾添加 `connect`，所以 `connection.args` 只填写附加
参数，不要再写 `connect`。启用连接时必须配置至少一个 TCP `readiness` 端点；若
端点已经可达，Conven 会复用现有网络，不再启动新的连接进程。

Conven 对自己启动的连接使用当前用户范围的全局锁、持久连接记录和 workspace 租约。
多个 workspace 复用同一个 ktctl 连接时，释放其中一个 workspace 不会中断其他租约；
最后一个租约释放后才会停止连接。若端点由 Conven 之外的网络或进程提供，Conven 只复用
可达性，不会取得所有权。需要以 root 启动自定义连接时可设置 `connection.sudo: true`；
Conven 会先执行交互式 `sudo -v`，再通过 `sudo -n` 启动并跟踪实际连接子进程，而不是
只记录外层 sudo 进程。停止时如果 sudo 时间戳已过期，Conven 会再次请求授权。
密码输入始终由 sudo 在终端中安全读取且不会回显；授权成功后 Conven 会输出明确的
完成提示。

## 本地与远程路由

假设本次选择 `user-svc` 和 `order-svc`：

```text
user-svc -> order-svc   使用 user-svc.dependencies.order-svc.localEnv
user-svc -> payment-svc 使用 user-svc.dependencies.payment-svc.remoteEnv
```

Conven 支持两种显式路由契约：

1. 传统环境变量契约：根据依赖是否选中注入 `localEnv` 或 `remoteEnv`。应用必须读取
   这些变量。
2. Policy YAML 契约：dependency 声明目标服务的 `binding` 和具名 `port`；选中依赖时
   使用 `routing.localDependency`（例如 `replace` 为
   `127.0.0.1:${dependency.port}`），未选中时使用 `remoteDependency`（通常
   `preserve` 原有 Consul/Apollo 发现配置）。

第二种方式只改运行时副本 `.conven/runtime/current/configs/<service>`，不会修改业务
仓库中的 YAML。`environments.<name>.env` 适合放环境级公共配置；
`services.<name>.env`、`services.<name>.localEnv` 和 dependency 环境变量仍按层级
覆盖。被选中的依赖必须至少具有
一种本地路由契约，否则 Conven 在启动前拒绝含糊计划。

`connection.driver: ktctl` 只解决本机到集群网络的可达性。它不代表集群服务可以
访问本地进程，也不替代注册中心、配置中心或业务鉴权。

## ktctl executable 选择

使用 `connection.driver: ktctl` 时，Conven 按以下优先级选择 executable：

1. workspace `.conven/config` 中的 `ktctl.path`。
2. `~/.conven/config` 中的 `ktctl.path`。
3. 当前 environment 的 manifest `connection.command`。
4. 通过 `PATH` 解析的 `ktctl`。

例如，可把当前机器专用的二进制路径留在共享 manifest 之外：

```bash
conven config ktctl.path /absolute/path/to/ktctl
# 或为所有 workspace 设置默认值：
conven config --global ktctl.path ktctl-custom
```

该设置只作用于 `ktctl` driver。`command` driver 始终使用 manifest 中自己的
`connection.command`，不受 `ktctl.path` 影响。启动前 Conven 会把 PATH 中的命令解析为
实际 executable 路径，因此即使 sudo 使用受限的 `secure_path`，
`connection.sudo: true` 也能找到该 ktctl。manifest command 如果是带路径分隔符的
相对路径，会先从 workspace 根目录解析。

## kubeconfig 传入与优先级

最终 kubeconfig 按以下优先级解析：

1. CLI `--kubeconfig FILE`。
2. `CONVEN_KUBECONFIG`。
3. `KTCTL_KUBECONFIG`，用于兼容现有脚本。
4. 当前 environment 的 `connection.kubeconfigEnv` 所指向的环境变量。
5. 本地 `.conven/config` 覆盖 `~/.conven/config` 后的有效 `ktctl.kubeconfig`。
6. 当前 environment 的 `connection.kubeconfig`。
7. `KUBECONFIG`。
8. `$HOME/.kube/config`。

示例：

```bash
CONVEN_KUBECONFIG=/secure/dev-kubeconfig \
  conven doctor --env dev --context dev-cluster --namespace dev

# 需要先在 manifest 中声明 environments.test profile。
KTCTL_KUBECONFIG=/secure/test-kubeconfig conven doctor --test

conven config ktctl.kubeconfig /secure/dev-kubeconfig

conven services --start --dev \
  --context dev-cluster \
  --namespace dev \
  user-svc order-svc
```

当 `connection.sudo: true` 时，最终启动形式为
`sudo -n <resolved-ktctl> --kubeconfig <file> ... connect`；Conven 会先执行交互式
`sudo -v` 授权。因此 `KTCTL_KUBECONFIG` 是直接支持的输入，不需要手工塞进
`connection.args`。

v1 要求 kubeconfig 解析结果是单个文件。`KUBECONFIG` 的多文件列表会被拒绝，避免
把列表直接交给不支持 Kubernetes 合并语义的连接工具。kubeconfig 中可能含凭据，
不要提交到 manifest 或仓库；优先使用 CLI 或环境变量传入个人路径。

## 运行状态与日志

workspace 运行状态固定放在 manifest 旁边，不再由 manifest 或用户状态环境变量选择：

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

Policy 重置前快照不属于 runtime，而是保留在 `<workspace>/.conven/backups/`；该目录
会被 workspace `.gitignore` 忽略，start、restart、stop 都不会自动删除。确认重置后的
声明已经审阅并提交或另行备份后，再手工清理。

运行目录位于规范化后的 workspace 内，因此两个同名 workspace 仍会自然隔离；通过
符号链接进入同一个 workspace 时，也仍会解析到同一份 runtime。

`.lock` 和 `connection.log` 位于 `current` 外，因此全新 start 可以持有 workspace 锁并
安全重建 `current`，而不会删除稳定的连接日志。只有 Conven 确实从当前 workspace 新建
连接时才会截断 `connection.log`；复用受管的共享连接时会保留原日志及其记录的连接
所有者 workspace 路径；复用外部网络路径不会创建或修改该文件。

重建 `current` 前 Conven 会核对已保存的 PID/PGID 和进程身份；存在活动进程或身份不可信
的进程时会拒绝启动，并保持所有运行文件不变。运行目录使用仅当前用户可访问的目录
权限，session、lock 和日志文件使用仅当前用户可访问的文件权限。删除或重建
`current` 前，Conven 会拒绝符号链接、非目录以及超出规范 workspace runtime 的路径。

`doctor`、start dry-run 和 status 都会显示这一固定路径。`.conven` 下的运行时变化不参与
源码 fingerprint，不会触发 restart。Conven 不创建历史运行目录：restart 复用
`current`，stop 保留它供排查，下次安全的 `services --start` 再替换它。

manifest schema 已删除 `workspace.stateDir`，继续声明会作为未知字段被拒绝。Conven 不会
发现、迁移或删除旧版本写入用户状态目录的 workspace 运行数据；只有在确认旧版本
进程均已退出后，才应手工删除这些数据。

当前用户的共享连接记录只位于 `~/.conven/state/connections`。Conven 不读取
`CONVEN_STATE_HOME` 或 `XDG_STATE_HOME`，也不发现或迁移旧的用户状态根。共享 registry
中不会保存 workspace 的 artifact、生成配置、服务日志、锁或 session 状态。

## 开发验证

在仓库根目录执行：

```bash
go test ./...
go vet ./...
go build ./cmd/conven
```

完整发布和 tap 验证流程见 [`RELEASING-ZH.md`](RELEASING-ZH.md)。

## License

本项目采用 MIT License，详见 [`LICENSE`](LICENSE)。版本变化记录在
[`CHANGELOG.md`](CHANGELOG.md)。
