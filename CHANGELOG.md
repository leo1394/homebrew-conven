# Changelog

## 0.2.9 - 2026-08-12

- Add the global `-C <path>` option before the command to run Conven as if it
  started in another working directory; update the man page and Bash, Zsh, and
  Fish completions for the new option.
- Change `conven --version`, `conven -v`, and `conven version` to print the
  version, release date, and canonical project URL as exact two-line output.

## 0.2.8 - 2026-08-12

- Add Homebrew bottle CI for macOS and Linux through `brew test-bot` and
  `brew pr-pull`, using GitHub Releases for bottle assets.
- Recommend the one-command, formula-scoped trusted install
  `brew install leo1394/conven/conven`; matching bottles no longer require Go
  on the client.
- Allow `--apply --bottle` to reuse the immutable source tag left by an earlier
  plain `--apply`, both before and after its Formula finalization completes.
- Service Selector 改用终端 alternate screen，确认或取消后不再把每次选择过程留在主屏
  scrollback；已选服务使用绿色背景，当前光标指向的已选服务使用红色背景，选中数量
  使用绿色高亮。操作提示精简为 `f`/`A`、Enter 和 `q`/Esc，原有方向键、`j`/`k`
  与 `F` 快捷键继续可用；空选择提示使用红色高亮。
- 二次确认改为明确的 `y`/`yes` 继续与 `n`/`no` 取消，大小写不敏感；无效输入会清空
  后重新询问，初次询问后最多重试三次，超限后终止启动。Selector 中 `q`/Esc 的取消
  行为保持不变。
- ktctl 在 Kubernetes Pod CREATE 请求返回 EOF 且进程错误地以成功状态退出时继续
  fail-closed：不自动重试、不保留连接租约，并明确提示远程 shadow pod 状态未知及需要
  检查的 namespace，避免输出具有误导性的 `exit status 0`。

## 0.2.7 - 2026-08-11

- 新增 `conven services --cleanup`：执行 `services --stop-all` 后清理当前 workspace 的
  构建产物和服务日志，保留运行配置与共享连接日志；存在 session、并发命令、symlink
  或越界路径时拒绝清理。
- 改进 CLI 参数诊断：Go `flag` 生成的未知参数、缺少参数值、非法值和帮助选项统一
  使用 `--name`；restart 收到 `--dev`、`--test` 或 `--env` 时，会在参数错误前高亮
  提示复用当前 session 环境，并给出对应的 `services --start` 用法。合法短选项
  `-h`、`-v` 保持不变。
- Dashboard 底部操作提示保持黄色，提示两侧的分隔线改为白色，与顶部一致。
- `services --start` 遇到可信运行 session 时，会先校验替换计划，再在交互终端提示
  `Stop then start` 或默认 `Cancel`；非 TTY 保持 fail-closed。确认后会在 workspace 锁内
  复核 session，若已变化则拒绝替换；配置匹配且 endpoints 可达的 managed ktctl lease
  会直接复用，不中断连接。

## 0.2.6 - 2026-08-11

- Dashboard 超出终端宽度的长日志按视觉行自动换行，不再用省略号隐藏内容，滚动和
  搜索可进入同一条日志的续行。Banner 顶部分隔线和字段标签改为白色，服务数量数字
  使用绿色，底部操作提示居中并使用黄色高亮。
- 交互式 `services --restart` 默认打开 Dashboard，非 TTY 时完成后返回；restart 新增
  显式 `--dashboard`，与 `--tail` 同时出现时以最后一个模式参数为准。

## 0.2.5 - 2026-08-11

- 调整日志查看语义：新增 `services --dashboard [service...]` 全屏查看器，并支持等效的
  `services --logs --dashboard [service...]` 别名；logs 下同时出现 `--dashboard` 与
  `--tail` 时以最后一个模式参数为准。固定 banner 下由应用最多保留最近 10,000 行
  聚合日志，并支持方向键、PgUp/PgDn、Home/End、`g`/`G` 滚动以及 `/`、`n`/`N`、
  Esc 搜索；Dashboard 搜索不依赖终端 scrollback。
  `services --logs --tail` 及 start/restart 的显式 `--tail` 改为使用终端主屏幕的 Plain
  持续日志流，可由原生 Command+F 搜索。交互式 start 默认打开 Dashboard，非 TTY
  start 默认返回；restart 默认返回且仅在显式 `--tail` 时连接 Plain 日志流。
- 将中英文 README 从实现手册重构为精简的开源产品门户，突出选择性本地编排、
  local/remote 混合路由、fail-closed 隔离边界及两条快速上手路径；完整命令细节继续由
  `conven help` 和 `conven(1)` 承载。

## 0.2.4 - 2026-08-11

- 修复合法整数 YAML mapping key 被本地隔离 guard 和 External Consul dependency
  preflight 全局拒绝的问题。整数 key 仅作为不透明业务数据保留；guard 路径及
  `discovType`、`consul` 等受检查字段仍只接受字符串 key，并继续 fail-closed 拒绝
  merge、自定义或 binary tag、其他类型 key、重复 key 和文本等价的类型冲突。
- 参照 Git 的顶层帮助结构精简 `conven --help`，按使用场景展示带说明的常用命令，
  将服务选择和 restart 行为说明收敛到 `conven services --help`；未知顶层命令会给出
  Git 风格的相似候选，但不会自动执行猜测结果。新增 `conven help <command>` 作为
  各公开命令详细帮助的统一入口，并为 `services` 动作补充用途说明及三种 shell 补全。
- 新增 `conven(1)` 系统手册，Homebrew 0.2.4 stable 和 HEAD 安装后均可通过
  `man conven` 查看完整命令、运行目录、环境变量和本地隔离安全边界。
- 修复 connection readiness 取消与连接进程同时退出时的清理 TOCTOU；仅在确认整个
  PID/PGID 已消失时归一为成功，真实残留进程组仍按 fail-closed 返回恢复状态。

## 0.2.3 - 2026-08-11

- 改进 ktctl connection 失败诊断：观察实际启动命令的退出状态，并在提前退出或
  readiness 超时时输出清理前的 endpoint 状态和去除控制字符后的 ktctl 日志尾部；
  日志不做凭据脱敏且只对内置 ktctl 自动输出。短超时会给出 120 秒 pod 创建、30 秒
  端口转发和 240 秒总预算建议。`POST /pods` EOF 结果不明确，继续 fail-closed，
  不自动重试。
- HTTP 服务的本地隔离输出现在将分离的 `host` 和 `port` 合并为完整 loopback
  监听地址；仅修正计划、启动校验和日志展示，不改变配置物化及隔离规则。
- 修复 macOS 回滚测试的时序竞态，并兼容新版 Homebrew 对 Formula 测试文件覆写的
  保护，恢复 stable/HEAD Formula 测试。

## 0.2.2 - 2026-08-11

- `conven plugins --install` 遇到同名插件时只在交互终端询问是否覆盖；仅 `y/yes`
  原子替换，其他输入取消，非 TTY 环境 fail-closed 保留原文件。新增
  `conven plugins --remove NAME`，按规范化名称删除一个真实普通插件，并同步三种 shell
  completion 和中英文文档。
- 破坏性变更：所有 `kind` 非空的本地服务都必须解析到 policy-backed 隔离契约；只有
  `kind` 为空的 runner-only 服务可继续单独使用 `localEnv`。最终 guard 强制关闭适用的
  服务注册、将 listener 限制为 loopback，并在启动前复核物化结果；当前可信语义限定为
  go-zero Consul application 根节点的 `discovType: ""`、RPC `listenOn` 和 HTTP `host`。
  Run argv 只允许 executable 加一个受信 `-f`；目录模式同时 guard local profile 的
  `config-local.yaml` bootstrap→application 链。Guard 文件路径逐级拒绝 symlink；兼容
  application 和受 guard 保护的 YAML 拒绝 merge、自定义 tag、非字符串及重复 key
  等解析歧义。
- 区分 manifest 声明的 `Declared remote dependencies` 与最终配置中检测并验证的
  `External Consul dependency preflight`，计划输出不再用 `via registry` 暗示传输方式。
  预检当前只识别已知 go-zero YAML 结构并调用明文 HTTP Consul health，不支持 ACL、
  TLS 或其他认证；预检通过后不会改写远程数据库、Kafka、RPC client 或后台 job 配置。
- 对 `connection.driver: command` 采取 fail-closed：底层配置模型仍可解析该 driver，
  但因无法证明任意 command 不建立反向入站 route，`doctor`、start dry-run、start 和
  restart 都会在修改进程前拒绝它；本地服务编排只接受 `none` 或内置 `ktctl` connection。

## 0.2.1 - 2026-08-07

- 破坏性变更：将 workspace、manifest、配置和运行时环境变量完整迁移为 `.conven`、
  `.conven/conven.yaml`、`~/.conven/config` 和 `CONVEN_*`；旧命名不再作为运行协议使用。
- 将用户级配置、共享连接 registry 和插件统一收敛到
  `~/.conven/{config,state/connections,plugins}`，不再读取 `CONVEN_STATE_HOME`、
  `XDG_STATE_HOME` 或创建 `$HOME/.local/state/conven`，也不迁移旧用户状态目录；连接
  状态路径逐层拒绝符号链接并强制使用 `0700` 权限。
- 新增 `conven plugins --install <python-file>|--list|--run`，从本地文件以非覆盖方式
  安装用户插件；保留通用内置插件的嵌入与 `init` 安装机制，但不在 Conven 仓库或
  二进制中打包项目专用脚本。执行时传入规范 workspace 路径并原样转发插件参数和
  终端输入输出，`--workspace` 保留给 Conven 且不可被覆盖。
- 重排 services 运行输出：绿色 `==>` 只标识阶段，二级详情使用无色缩进，青色只标识
  服务名和关键值，黄色与红色分别专用于告警和失败。
- 修复交互式 `sudo -v` 被放入后台进程组后密码明文回显且无法完成认证的问题，并增加
  密码隐藏提示与认证完成反馈。

## 0.2.0 - 2026-08-07

- 将项目、Homebrew tap、Formula 和 CLI 从 `homebrew-loom`/`loom` 改名为
  `homebrew-conven`/`conven`，并为旧 tap 中的 `loom` Formula 增加跨 tap 迁移映射。
- 继续兼容既有 `.loom` workspace、`.loom/loom.yaml`、`~/.loom/config`、`LOOM_*`
  环境变量和用户级 `loom` 状态目录；本次只迁移外部品牌与命令，不迁移持久状态协议。

## 0.1.1 - 2026-08-07

- 增加磁盘空间检测
- 日志增加高亮显示

## 0.1.0 - 2026-08-07

- 将服务操作统一收敛到 `loom services`，动作参数必须是其后的第一个
  参数，并且必须且只能指定 `--list`、`--registry`、`--status`、`--logs`、`--start`、
  `--restart`、`--stop`、`--stop-all` 之一。旧的 `loom looming` 以及顶层 `list`、
  `discover`、`status`、`logs`、`start`、`restart`、`stop` 命令不再接受。
- 破坏性变更：移除 `services --start`、`services --restart` 和 `services --logs` 的
  `--follow`，统一改为布尔开关 `--tail`，旧参数不再接受。
- 破坏性变更：移除 `services` 和 `doctor` 的 `--workspace` 和 `--config`，并停止把
  `LOOM_WORKSPACE` 作为 CLI 工作区覆盖。工作区仅从 cwd 向上发现最近的
  `.loom/loom.yaml`；最近的 `.loom` 若不完整则直接报错，不回退到父工作区。
- 用户 HOME 下的 `~/.loom` 专用于全局配置，不作为 workspace 边界；`loom init`
  会拒绝直接在 HOME 初始化，避免本地与全局 `.loom/config` 指向同一文件。
- 本地服务仍会收到 Loom 注入并覆盖继承值的 `LOOM_WORKSPACE`，用作当前
  工作区绝对路径的只读元数据。
- 增强 `loom init`：首次初始化会扫描 workspace 的一级子 Git 仓库；新增可扩展的
  `RepositoryAnalyzer` seam，以及 `go-root-module`、`go-subdirectory-module` 两个
  analyzer，分别识别根目录和 `go/` 子目录中的 Go main module。除 `path`、
  `workdir`、`build`、`run` 外，还会通过 Go AST 保守提取 go-zero HTTP/RPC kind 和
  显式 YAML tag 对应的 RPC client binding 候选；多个 analyzer 同时命中会明确报冲突。
  无强匹配时回退
  `examples/application.yaml` 内置示例；语法未完成或缺 module directive 的 WIP
  仓库只会被跳过，不阻断其他仓库。init 通过原子 no-replace 发布，重复执行及并发
  创建都不会覆盖已有 manifest。
- 添加 `loom services --registry [--prune]`：从 workspace 子目录调用时仍扫描
  workspace 根目录，按仓库路径增量添加 service；对已有同路径 service 只补缺失的
  `kind` 或整段缺失的 `discovery`，人工非空字段、runner 和 YAML 注释优先。默认保留
  扫描中缺失的条目，`--prune` 显式同步删除，并在依赖将失效时拒绝写入。
- discovery 从同一份 manifest 源 bytes 解码合并，最终 YAML 严格校验后还必须与已
  校验的强类型 candidate 完全一致，防止 YAML `<<` merge key 使 prune 表面成功但
  语义未生效。紧邻 rename 前会 best-effort 复查 same-file 与源 bytes，已检测到冲突
  时中止；该检查不是对任意外部 writer 的线性化 CAS，仍存在极窄的 check/rename
  窗口。为避免原子 rename 替换链接本身，拒绝符号链接 manifest。
- 明确自动发现只推导可信运行入口及少量静态描述，不推断端口、完整依赖图、环境
  变量、Apollo 凭据、公司 policy 或集群连接；被发现不代表服务已具备端到端联调配置。
- 添加 `loom policy --edit` 和显式破坏性的 `loom policy --reset`。前者只在
  临时草稿通过严格校验且源 manifest 未发生已检测到的并发变化后原子发布；后者可从
  只读仓库扫描重建缺失、损坏或需要重置的中央 manifest，零匹配时拒绝覆盖。重置
  前及被拒绝的已变化草稿会以私有权限保存在 `.loom/backups/`，且不会写入子业务仓库。
- 添加 `loom policy --import <yaml-file> [--edit]`：把本地生成的完整 Loom v1 manifest
  原始 bytes 整体发布到当前 workspace，或先在不修改源文件的私有草稿中审阅。相对
  源路径从调用 cwd 解析，旧 manifest 会先备份；该动作不做字段级 merge。Schema 和
  语义校验成功不代表服务可运行，导入后仍需执行 doctor、start dry-run 和业务验证。
- 移除内置的项目专用 policy 模板及 `loom policy --use-template`，避免项目配置被打包进
  源码、示例或二进制；项目生成器输出统一通过 `loom policy --import` 显式导入。
- 将 `.loom/loom.yaml` 扩展为集中式 workspace 自描述：新增 workspace/service policy、
  `policies.*.drivers/config/process/routing`、service `kind/discovery/config` 和 dependency
  `binding/port`。发现得到的 service 描述只写中央 manifest；init 另在中央
  `.loom/.gitignore` 合并 runtime 规则，不向子业务仓库写 Loom 配置。
- 添加 `repository`、`apollo` config source 和 `yaml-overlay` materializer：从只读
  service 配置源复制/获取 application，在 staging 中应用 policy、server、service 和
  dependency patches，校验后原子发布到
  `.loom/runtime/current/configs/<service>`。支持 policy 追加进程环境与 argv。
- 添加 policy 驱动的本地/远程 YAML 路由：选中的 dependency 可按 `binding` 和具名
  `port` 替换为本地 target，未选中的 dependency 可保留 Consul/Apollo 远程发现配置；
  现有 `localEnv`/`remoteEnv` 契约继续支持。
- 添加类 Git 的本地 `.loom/config` 与全局 `~/.loom/config` 管理，以及
  `ktctl.path` 可执行文件和 `ktctl.kubeconfig` 凭据路径覆盖配置。
- 为 `services --start` 和 `doctor` 添加 `--dev`、`--test` 环境快捷参数；同值 `--env`
  可组合，冲突值会在启动 workspace 前报错。
- 明确支持 `KTCTL_KUBECONFIG`；sudo 连接使用 PATH 解析后的 ktctl 绝对路径，最终
  通过 `sudo -n <ktctl> --kubeconfig <file> ... connect` 启动。
- 添加 `loom services --restart`：无 service 参数时仅重启源码或解析后的运行/配置计划
  已变化的本地服务，显式传入 service 时强制重启指定服务。restart 复用固定
  `current` 和共享连接，只重新物化、prepare/build/start 目标；未变化服务的 artifact、
  config 和日志保持不动，目标日志追加 restart marker。
- 添加 `runner.runWorkdir`：prepare/build 继续使用 `runner.workdir`，服务进程与
  `command` 健康检查使用独立的模板化运行目录；相对路径基于 service 目录解析，
  目录可由 prepare 创建。启动前会验证目录，restart 在停止旧进程前完成验证，且
  run workdir 会纳入计划 fingerprint。
- 添加 manifest 驱动的多语言本地服务启动流程。
- 将 workspace 状态固定到 canonical workspace 的 `.loom/runtime`，移除 workspace
  hash、历史 `runs/<run-id>` 和 `workspace.stateDir`。fresh start 在锁和进程身份校验后
  重建 `current`；stop 和失败回滚保留诊断文件，restart 原地复用。用户状态目录仅保留
  跨 workspace 的小型 connection registry。
- 明确 `services --start --dry-run` 只做本地只读静态计划校验，不创建/清理 runtime，
  不访问 Apollo、不建立网络连接，也不执行 materialize、prepare、build 或服务命令。
- 添加本地/远程依赖环境变量路由和 ktctl 连接配置。
- 添加无 service 参数时的 PathPicker 多选与显式确认。
- 添加 `services --list`、`--status`、`--stop`、`--logs`，以及独立的健康检查命令
  `doctor`。
- 添加 `--tail` 全屏日志 Dashboard，可由 `services --start`、`--restart` 或 `--logs`
  使用：固定展示 workspace/environment、已启动服务、
  manifest 声明端口快照和当前 LAN IPv4，剩余区域持续聚合日志；支持窗口 resize、
  ANSI 控制序列净化，以及通过 `q`/`Ctrl-C` 脱离而不停止服务。非 TTY 或管道输出
  降级为带 `[service]` 前缀的普通文本流。
- 添加循环依赖分组启动，以及上下文取消时的进程组清理。
- 添加当前用户状态目录内的共享连接租约、陈旧租约回收和显式强制恢复。
  `services --stop-all` 与 `services --stop --all` 进入同一清理路径，只释放当前
  workspace lease，并仅在没有其他活动 lease 时终止 ownership 记录确认的 ktctl
  connection；同时为 `Owned=false` 且 `Managed=false` 的外部 ktctl/网络可达性只清理
  session 引用，不会被 Loom 终止。
- 添加 PID/PGID/启动身份校验，并支持 sudo 连接的授权刷新。
- 添加 Homebrew HEAD Formula 及包含全部 `services` 动作的 Bash、Zsh、Fish 补全。
