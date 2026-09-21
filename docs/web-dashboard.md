# Web dashboard / 本地诊断工作台

```bash
conven services --start --test --dashboard --web svc1 svc2
conven services --dashboard --web
conven services --dashboard --web svc1
conven services --diagnose
```

`--dashboard --web` reuses the saved session, including its environment. Service
names filter the view; they do not start or restart anything. In an interactive
terminal it also keeps the TUI log dashboard open; Web is an additional view, not
a replacement. Without a terminal or session, only Web opens. `--diagnose` opens
diagnostics, including failed attempts before a session existed. Do not combine
it with a start/stop action. `--web` on start requires `--dashboard` and excludes
`--tail` and `--dry-run`. Existing terminal dashboard commands remain available.

`--dashboard --web` 复用已有 session 的服务与环境，服务名仅用于过滤显示。
交互终端中同时保留 TUI 日志与 Web；无终端或无 session 时仅打开 Web。
退出 TUI 或关闭浏览器均不会停止业务服务，`--diagnose` 仍仅打开 Web 诊断页面。
`--diagnose` 查看诊断历史，不隐式启动服务；首次启动失败也可查看。
启动时 `--web` 必须搭配 `--dashboard`，不能与 `--tail`、`--dry-run` 混用。

## What the evidence means / 状态含义

- **Process**: current saved-process identity and liveness, not application health.
- **Startup isolation**: listener/registry evidence captured during startup, with
  its timestamp. Continuous isolation observation is not enabled in this version.
- **Current health**: bounded HTTP/TCP probes, independent of startup evidence.
  Missing checks, stale data and unsupported command checks remain unknown.
  Unhealthy observations never automatically restart or stop a service.
- **Routes**: captured configuration routes, not distributed tracing or measured
  traffic. A later manifest edit does not rewrite the launch-time evidence.
- **Logs**: existing request/trace identifiers can correlate entries across local
  services. Conven does not inject tracing into applications or collect cluster logs.

进程存活不等于健康；启动隔离验证不等于持续隔离监控。网络检查失败也不等于
已经发生注册泄漏。路由图展示配置关系，不展示推测的实时流量或 QPS。
日志关联使用应用已有的 request/trace ID，不自动改动业务源码。

The overview includes the same LAN address/interface and disabled binding list as
the TUI. Green map nodes run locally; amber dashed nodes represent remote
dependencies, not verified remote health. Arrows point from provider to consumer
(B → A means B serves A; A depends on B). Providers and consumers use separate
layers and fan-out ports so shared providers do not appear as duplicate dependencies.
Each section can collapse independently; following
a log or navigation link expands its destination. The log viewport is at least
75% of the browser viewport height.

概览补充与 TUI 一致的 LAN 地址、网卡和 disabled bindings。拓扑中绿色表示本地运行，
琥珀色虚线节点表示远程依赖，不代表远程健康已验证；B → A 表示 B 为 A 提供服务（A 依赖 B）。
提供方与调用方分层排列，共享提供方使用独立连线端点，避免误读为重复依赖。
各分区可独立折叠，跳转时自动展开目标分区；日志显示区至少占浏览器窗口高度的 75%。

Logs has a **Full screen** button to fill the browser viewport while retaining
search, filters and follow controls. **Escape** or **Exit full screen** restores
the dashboard layout. Scrolling inside logs does not scroll the outer page when
the log boundary is reached, in either mode.

Logs 的 **Full screen** 按钮将日志区铺满浏览器窗口，保留搜索、筛选和跟随操作；
按 **Esc** 或 **Exit full screen** 恢复原布局。普通与全屏模式均隔离日志区滚动，
到达日志边界时不继续带动外层页面。

## Lifecycle and access / 生命周期与访问

Go serves embedded assets on a random `127.0.0.1` port. A workspace reuses one
authenticated detached Conven viewer. No Node, package downloads or permanent
OS daemon is required. The viewer exits after 30 minutes without clients or
operations. Closing the browser or exiting the viewer leaves business services
running. Run the dashboard command again to reopen it.

`conven services --stop-all` also shuts down the current workspace's Web viewer,
even without a service session. Stopping selected services keeps the viewer open.
An active Web action must finish before viewer shutdown. Older viewers without
the shutdown endpoint must be closed manually once and reopened with the new binary.

`conven services --stop-all` 同时关闭当前 workspace 的 Web 后台，即使没有业务
session；停止指定服务则保留 Web。正在执行的 Web 操作完成后才能关闭后台。
旧版后台不支持关闭接口时，需手动关闭一次，再使用新版命令重新打开。

访问 URL 包含私有令牌，请勿转发或放入工单。后端只监听本机并校验 Host、Origin
及认证令牌；写操作额外校验请求令牌和当前 session，页面过期时必须刷新。
停止或重启需要明确选择服务并确认，通过现有编排流程和锁执行。

Logs and diagnostics are redacted before browser presentation, but redaction is
not a substitute for avoiding secrets in application logs. Local original log
files retain the application's original content. Treat the workspace as private.
The dashboard has no arbitrary command or file-reading endpoint.

## Retention / 保留范围

Startup attempts are bounded and stored separately from the replaceable current
runtime, so a failed new start does not impersonate an older running session.
Diagnostic history is local, not a monitoring backend or permanent log archive.
Old raw application logs may be replaced by a later fresh start; retained failure
snippets provide context without promising unlimited history.

The timeline also shows up to 200 process, health and connection-state transitions
observed by the current viewer. These observations are not a persistent audit or
proof of continuous isolation; startup/restart attempts are retained separately.

## Release verification / 发布检查

The ordinary Go test, race, vet and build gates cover the embedded server and
assets. Formula and installer still build a single Go executable; no frontend
runtime installation is added. Network/cluster startup remains a separate manual
acceptance check, not a requirement for packaging the browser viewer.
