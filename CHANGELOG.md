# Changelog

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
