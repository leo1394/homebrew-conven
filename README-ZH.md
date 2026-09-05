# Conven

[English](README.md) | **简体中文**

[![CI](https://github.com/leo1394/homebrew-conven/actions/workflows/ci.yml/badge.svg)](https://github.com/leo1394/homebrew-conven/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

![Conven——解决本地开发与集群环境隔离的痛点](assets/conven-banner-zh.png)

> **可验证的本地微服务编排工具：**明确选择本地服务，显式连接其余依赖，并持续验证运行时隔离。

Conven 是一个专注于本地开发的微服务编排工具。它选择一组服务在本地运行，并通过
配置的 Endpoint 或开发集群访问其余依赖。生成配置始终保存在服务源码仓库之外。

- **启动最少服务：** 只运行当前改动涉及的服务。
- **保留真实拓扑：** 本地服务可以继续访问远程 RPC、数据库、Kafka、配置中心
  和其他开发环境依赖。
- **安全校验不通过即拒绝启动：** 对于支持的类型化服务，Conven 必须确认本地
  注册和监听地址已经隔离。
- **不限开发语言：** prepare、build 和 run 都是 argv 数组，并非 Go 专用钩子。

## 为什么使用 Conven

在笔记本上启动整套微服务通常很慢，也没有必要。只启动一个服务虽然更快，
但配置、服务发现、本地路由、日志和进程清理往往会变成一组项目专用脚本。

Conven 将这些约定集中到一份经过确认的 workspace manifest 中：

```mermaid
flowchart LR
    M[".conven/conven.yaml"] --> P["编排计划 + 安全校验"]
    P --> R[".conven/runtime/current"]
    R --> A["本地 API"]
    R --> B["本地 RPC"]
    E["已配置的本地 Endpoint"] --> A
    E --> B
    A -->|"127.0.0.1"| B
    A -->|"ktctl + 远程服务发现"| D["开发环境依赖"]
    B -->|"ktctl + 远程服务发现"| D
```

源码仓库仍然只用于编辑代码。运行时 YAML 副本、构建产物、日志和 session 状态
都位于 workspace 的 `.conven/runtime` 下。

## 安全设计

对于声明了 `kinds: [http]`、`kinds: [rpc]` 或多个 listener 的服务，只有可信
Adapter 能验证最终
运行计划时，Conven 才会启动：

- 已禁用远程服务注册，或该服务类型明确不需要注册；
- 服务监听地址使用声明的范围，默认绑定到 loopback IP；
- run argv 指向 Conven 保护的运行时配置；
- 集群连接不会建立从集群到本机的入站路由。

任何证明缺失或含糊时，启动都会按 fail-closed 原则失败。Analyzer 只提取源码事实，
Certifier 将事实与唯一匹配的 Policy 编译为可信运行契约；核心编排只消费该契约，
不按框架分支。启动后还会验证 listener 归属和注册中心增量。完整的框架、配置交付与
注册中心支持范围见[类型化服务支持矩阵](docs/typed-service-support-zh.md)。Conven 能
验证运行计划和可观察结果，但无法证明任意二进制一定会遵守传入的参数。

typed service 默认只监听 loopback。如果需要让同一局域网内的其他设备访问某一个
服务，可只为该服务显式开放所有网卡：

```yaml
services:
  portal-api-service:
    kinds: [http]
    network:
      listen: all-interfaces
```

Conven 会为该服务强制写入 `0.0.0.0`，并在启动计划中输出警告。`0.0.0.0` 表示主机
所有网卡，并非只开放局域网网卡；最终可达范围仍由系统防火墙和网络环境控制。本地服务
路由和健康检查仍使用 `127.0.0.1`。不配置 `network.listen` 或显式设为 `loopback`
时保持默认的仅本机访问；不接受任意自定义监听地址。

也可以通过 services 命令按服务开关：

```bash
conven services --listen --on portal-api-service
conven services --listen --off portal-api-service
```

命令会原子更新 `.conven/conven.yaml`，但不会隐式重启正在运行的进程。新的监听范围在
下一次 `services --start` 或 `services --restart` 时生效。

Conven 内置的 materializer 只将生成的 YAML 写入
`.conven/runtime/current/configs/<service>`，不会覆盖仓库内的 YAML。fresh start
会先核验已保存的进程身份和运行目录，再清理 `current`。stop 和 rollback 在向
进程组发送信号前，也会验证 PID/PGID 的归属。如果无法确认清理完成，Conven
会保留 session，并阻止下一次 fresh start。

> **本地服务隔离不等于数据隔离。** 本地服务仍会使用运行时配置中的远程
> 数据库、Kafka、未选中的 RPC 客户端和后台任务，因此可能写入数据或消费消息。
> Conven 不会隔离这些副作用。在统一异步工作负载本地路由实现前，
> `SERVICE_KAFKA_CONSUMERS_ENABLED` 默认取 `true`，也不强制源码实现 guard。只有显式
> 设置为 `false` 请求隔离 Kafka consumer 时，Conven 才会在启动前验证服务能否响应该开关。

未声明 `kinds` 的 runner-only 服务不具备相同的 Adapter 安全保证。项目自定义的
`prepare` 和 `build` 命令也会以当前用户权限运行，并可能修改其工作目录。

## 安装

### Homebrew（推荐）

```bash
brew install leo1394/conven/conven
```

安装完成后，升级可以直接使用短名称：

```bash
brew update
brew upgrade conven
```

### Bash

如果本机 Homebrew 版本过低，无法安装 Formula，可通过 Bash 构建并安装已发布
版本：

```bash
curl -fsSL https://raw.githubusercontent.com/leo1394/homebrew-conven/master/install.sh | bash
```

Bash 安装需要 `curl`、`tar` 和 Go 1.23 或更高版本。脚本会使用仓库发布的
SHA256 清单校验源码归档，构建 Conven，并安装到 `~/.local/bin`。如果脚本提示
该目录不在 `PATH` 中，请按提示添加；再次执行同一命令即可升级。也可以指定版本
或安装目录：

```bash
curl -fsSL https://raw.githubusercontent.com/leo1394/homebrew-conven/master/install.sh | CONVEN_VERSION=1.0.2 bash
curl -fsSL https://raw.githubusercontent.com/leo1394/homebrew-conven/master/install.sh | CONVEN_INSTALL_DIR=/absolute/bin bash
```

Conven 支持 macOS 和 Linux。Homebrew 和 `install.sh` 只安装 Conven，不自动安装
项目使用的语言运行时和包管理器。只有环境使用 `ktctl` connection driver 时才需要
安装 `ktctl`；匹配 Homebrew bottle 时，客户端不需要安装 Go。

## 快速上手

### 从 0 开发：没有集群和远程依赖

先创建 `local` 环境并查看当前可用的 Service：

```bash
cd /path/to/workspace

conven init --local
conven services --list
```

明确启动多个本地服务时直接列出名称；需要递归加入
本地 Service 依赖时使用显式选项 `--with-dependencies`：

```bash
conven services --start user-svc order-svc
conven services --start order-svc --with-dependencies
```

默认只启动显式选中的 Service。将本地 Service 所需的连接地址声明为环境 Endpoint，
再映射给使用它的 Service：

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

Conven 会在启动 Service 前检查其引用的 Endpoint。只有一个环境时无需反复传
`--env local`：

```bash
conven doctor --env local
conven services --start user-svc order-svc
conven status
```

完整步骤见
[纯本地新手手册](docs/getting-started-local-zh.md)。

### 使用已有 Conven 配置的项目

如果项目已经提交 `.conven/conven.yaml`：

```bash
cd /path/to/workspace

conven doctor --test
conven services --start --test portal-api-service partner-service visit-plan-mgr-service
```

1.0 使用 Manifest v3。旧的 v1/v2 workspace 需要先停止运行中的 session，再执行
`conven workspace --migrate`；Conven 会在备份和完整校验后原子替换 manifest。
无法证明 registry identity、runtime 或 Policy 时迁移会整体失败并保留原文件。

请将示例服务名替换为 `conven services --list` 输出的名称。在交互式终端中也可以
省略服务名，通过选择器选择。启动完成后，Conven 默认打开 Dashboard。按 `q`
或 `Ctrl-C` 只会退出查看，服务会继续运行。
![Conven services Dashboard](assets/conven-services-dashboard-snapshot.png)

需要显式停止整个 workspace session：

```bash
conven services --stop-all
```

### 首次接入项目

在包含各服务仓库的目录中运行 `init`：

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

`init` 会完成首次一级子仓库 registry 扫描，并初始化以下文件：

| 文件 | 用途 |
| --- | --- |
| `.conven/conven.yaml` | 定义环境、服务、Policy 和运行行为的规范 workspace manifest。 |
| `CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md` | 用于实现 workspace Policy 生成器插件的 AI 可读规范。 |
| `README.md` | 介绍生成文件和 Conven 工作流的 workspace 本地快速上手文档。 |

每个文件仅在缺失时创建；已有普通文件保持不变。`.conven/conven.yaml` 是唯一服务清单。
`init` 和后续的 `services --registry` 只做静态扫描，不运行构建、不访问网络；它们记录
可以证明的 runner、listener、注册代码、binding 和健康检查，并从 `18080` 开始为新服务
分配最低未占用端口，已有端口不重新编号。已支持框架但证据不足时 registry 原子失败，
完全不支持的仓库则以具体原因列入 skipped repositories。

Conven 支持 Go、Spring Boot、Python、Node.js 和 Bun 的常见 HTTP/RPC 框架，以及
passive、Kubernetes DNS、Consul、Nacos、Eureka 和 Etcd 契约。它不会猜测完整业务
依赖图、公司 Policy、凭据或集群连接信息。具体边界和修复示例见
[类型化服务支持矩阵](docs/typed-service-support-zh.md)与
[服务运行时配置契约](docs/service-runtime-config-contract-zh.md)。

如果项目维护了 Policy 生成器，请安装后运行唯一的 workspace 插件，或显式指定名称。
首次运行前还需按生成的 AI 规范补全 workspace 专用的 `conven-generator.json`；`init`
不会猜测环境或连接策略：

```bash
conven plugins --install ./generate-workspace-policy.py
conven plugins --run --output
conven workspace --import --edit
```

Conven 当前不内置任何项目专用插件。`workspace --import` 会验证并替换完整 manifest，
并非 YAML merge。

## 一次启动如何完成

1. 查找最近的 `.conven/conven.yaml`，并解析选中的环境。
2. 选择服务，解析每条 dependency edge，并按依赖顺序生成启动分组。
3. 验证本地 Service、Endpoint、remote、disabled 路由，以及隔离契约、运行时配置消费、
   本地 module replacement、命令和路径。
4. 检查被引用的外部 Endpoint 是否就绪。
5. 复用或建立环境连接，在 `.conven/runtime/current` 生成运行时配置。
6. 执行 prepare、build、start 和 health check，再验证 listener 归属与注册中心增量，
   最后记录状态和聚合日志。

### 配置物化顺序

第 5 步按两条相互关联的链路完成。以下 `runtime/current/application.yaml` 是单个服务
运行时配置副本的简写；实际文件位于
`.conven/runtime/current/configs/<service>/application.yaml`。Apollo 整体替换只作用于
这份受保护的运行时副本，不会覆盖仓库文件。

从仓库基线配置到远程依赖预检的完整链路：

```text
仓库 resources/application.yaml
  → Apollo application.yml 整体替换
  → Conven manifest patches
  → runtime/current/application.yaml
  → Consul preflight
```

当 Apollo `application.yml` 作为运行时基线时，patch 和安全 guard 按以下顺序应用：

```text
Apollo application.yml
  → policy patch
  → server patch
  → services.portal-api-service.config.patches
  → dependency routing patch
  → workspace.disabledBindings（仅处理实际存在的 binding）
  → 本地隔离 guard
  → runtime/current
```

第二条链路同时表示覆盖优先级：后面的 patch 基于前一步结果继续处理；
`services.portal-api-service.config.patches` 是服务级 manifest patch 的具体示例。
`workspace.disabledBindings` 只禁用拉取配置中实际存在的客户端，不会创建缺失的 binding。
本地隔离 guard 强制并校验最终监听和注册行为，Consul preflight 则检查最终运行时配置
中仍然启用的远程依赖。

### 服务运行时配置契约

> **重要：** typed service 必须接受 Adapter 声明的运行时配置参数，并实际从该路径
> 加载配置。仅接收、声明或忽略参数不符合 Conven 接入契约。参数缺失、路径无效或
> 配置解析失败时，服务必须以非零状态退出，不得回退读取源码目录中的默认配置。

Conven 不要求所有语言使用同一个硬编码参数。业务源码应保持工具无关，只识别框架或
项目自己的配置参数；Manifest/Policy 负责把 `${configDir}` 适配成对应 argv：

| 语言/框架 | 推荐运行时参数 |
| --- | --- |
| Go/go-zero | `-f <runtime-config-directory>` |
| Java/Spring Boot | `--spring.config.location=file:<runtime-config-directory>/` |
| Python | `--config <runtime-config-file>` 或 `--config-dir <runtime-config-directory>` |
| Node.js | `--config <runtime-config-file>` 或 `--config-dir <runtime-config-directory>` |
| Dart | `--config <runtime-config-file>` 或 `--config-dir <runtime-config-directory>` |
| Rust | `--config <runtime-config-file>` 或 `--config-dir <runtime-config-directory>` |

正确实现必须完成“解析参数 → 使用该路径加载配置”；解析后仍加载源码配置同样无效。
`CONVEN_CONFIG_DIR` 是 Conven 提供给 runner hook 和编排过程的运行时元数据，不应成为
业务服务源码的推荐接入 API。完整实现、错误示例和 canary 端口行为验证见
[服务运行时配置契约](docs/service-runtime-config-contract-zh.md)。

可信 Adapter 最后注入受保护的 host、port 和禁注册参数；缺失、重复或冲突时会在启动前
拒绝。业务包含手工注册逻辑时必须提供框架原生或中性的关闭开关，且不得依赖 Conven
专用变量。具体语言契约和修复示例统一放在
[服务运行时配置契约](docs/service-runtime-config-contract-zh.md)。

`services --start --dry-run` 在静态编排完成后结束。它不会访问 Apollo、建立连接、
生成运行时配置、构建代码、启动进程或修改 runtime 目录。

对于 manifest 中声明的依赖，是否被选中决定其路由：

```text
选中的依赖      -> manifest Policy 中声明的本地地址
未选中的依赖    -> 保留远程服务发现和配置
```

编排计划中的 **Declared remote dependencies** 只包含 manifest 明确声明的依赖，
不代表应用配置中隐藏的所有端点。对于兼容的 go-zero/Consul YAML，Conven 还会检测
启用的外部 Consul 客户端，并在服务启动前确认至少存在一个 passing 实例。

## Manifest

每个 workspace 只有一份规范 manifest：

```text
<workspace>/.conven/conven.yaml
```

它包含四个主要部分：

| 部分 | 描述 |
| --- | --- |
| `workspace` | 项目名和默认 Policy |
| `services` | 仓库路径、runner、端口、监听范围、健康检查和依赖 |
| `policies` | 框架/配置 driver、运行时 overlay、路由和隔离规则 |
| `environments` | 环境变量和可选的集群连接 |

最小的 runner-only workspace 如下：

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

该示例有意省略 `kinds`，因此只是通用 runner-only 配置。类型化 HTTP/RPC 服务必须
引用具有完整且可验证隔离契约的 Policy。包含多服务、依赖环境、健康检查和 `ktctl`
连接的配置可参考[示例 manifest](examples/application.yaml)。

Manifest v3 使用 `kinds`、`healthChecks`、命名 registry、明确的 discovery identity、
`providerAliases` 和 `consumerBindings`。每个 kind 必须拥有对应端口、Policy server
route 和 health check；同一进程可以暴露多个 listener。

命令使用 argv 数组。除非显式调用 shell，例如 `[sh, -c, "..."]`，否则管道、
重定向和 `&&` 不会被解释执行。

## 常用命令

| 操作 | 命令 |
| --- | --- |
| 查看完整 workspace 状态 | `conven status` |
| 编辑 workspace manifest | `conven workspace --edit` |
| 校验 workspace manifest | `conven workspace --validate` |
| 迁移旧 manifest | `conven workspace --migrate` |
| 列出 manifest 中的服务 | `conven services --list` |
| 刷新扫描到的服务仓库 | `conven services --registry` |
| 开放指定服务供局域网访问 | `conven services --listen --on SERVICE...` |
| 恢复指定服务仅本机访问 | `conven services --listen --off SERVICE...` |
| 验证指定环境 | `conven doctor --test` |
| 预览启动计划 | `conven services --start --test --dry-run SERVICE...` |
| 启动本地服务群 | `conven services --start --test SERVICE...` |
| 重启变化或已退出的服务 | `conven services --restart` |
| 查看当前 session | `conven services --status` |
| 查看日志快照 | `conven services --logs SERVICE...` |
| 打开 Dashboard | `conven services --dashboard SERVICE...` |
| 持续输出 Plain 日志 | `conven services --logs --tail SERVICE...` |
| 停止指定服务 | `conven services --stop SERVICE...` |
| 停止整个 workspace session | `conven services --stop-all` |
| 清理构建产物和服务日志 | `conven services --cleanup` |

使用 `--dev`、`--test` 或 `--env NAME` 选择 manifest 中声明的环境。如果启动时需要
覆盖当前机器的 Kubernetes 设置，可添加 `--namespace NAME`、`--context NAME` 或
`--kubeconfig FILE`。

fresh `--start` 会安全地重建 `runtime/current`。`--restart` 会复用该目录，只重启
发生变化或已经退出的服务；未变化的服务和共享连接会保持运行。stop 后仍会保留
当前日志和生成文件，供排查使用，直到下一次安全的 fresh start。

`--start` 和 `--restart` 还会监听运行中服务的源码变化。构建成功并通过 preflight
后执行受控替换；构建失败时保留 last-known-good 进程继续运行。`--stop-all` 会同时
终止 watcher。

执行 `--stop-all` 后，可使用 `--cleanup` 删除 `runtime/current/artifacts` 和
`runtime/current/logs`。存在 session 时会拒绝清理；运行配置和共享连接日志会保留。

## 日志

Conven 提供两种用途明确的日志查看模式：

| 模式 | 适用场景 | 行为 |
| --- | --- | --- |
| Dashboard | 实时概览 | 固定 workspace banner、长日志自动换行、应用内滚动和 `/` 搜索，最多保留 10,000 条原始日志 |
| Plain | 使用终端原生搜索或导出 | 使用正常终端 scrollback、`Command+F`、管道和重定向，follow 前最多回放 10,000 行 |

Dashboard 将 workspace、local services、disabled bindings、启动时间和实时日志
集中在一个视图。

```bash
# 全屏查看器；下一条命令是等价别名。
conven services --dashboard
conven services --logs --dashboard

# Plain 持续日志流。
conven services --logs --tail
```

交互式 start 和 restart 默认打开 Dashboard；`--tail` 使用 Plain 模式。Dashboard
支持日志自动换行、滚动和 `/` 搜索；按 `g` 或 `G` 跳到最新日志并持续 follow，Home
跳到最旧日志。按 `q` 或 `Ctrl-C` 退出查看，不会停止服务。

## 配置与插件

当前机器专用的 ktctl 设置应放在共享 manifest 之外：

```bash
conven config ktctl.path /opt/homebrew/bin/ktctl
conven config ktctl.kubeconfig /secure/dev-kubeconfig

# 为所有 workspace 设置默认值。
conven config --global ktctl.path ktctl
```

workspace 配置位于 `.conven/config`，全局配置位于 `~/.conven/config`，本地值覆盖
全局值。不要将 kubeconfig 文件和凭据提交到源码仓库。

使用以下命令管理本地 Python 插件：

```bash
conven plugins --install ./plugin.py
conven plugins --list
conven plugins --run --output
conven workspace --import
conven plugins --remove plugin

# 显式使用用户全局插件范围。
conven plugins --install --global ./plugin.py
conven plugins --list --global
conven plugins --global --run plugin --output
conven plugins --remove --global plugin
```

workspace 插件位于 `.conven/plugins`，全局插件位于 `~/.conven/plugins`，两层允许
同名。显式名称优先执行 workspace 插件，回退到全局插件前会 warning；workspace
只有一个插件时可以省略名称，存在多个时会打开单选器；workspace 没有插件时，单选器
会显示 global 候选，即使只有一个候选也需要确认。显式 global run 必须提供名称。
`--output` 不带文件名时会原样传给插件，生成器约定写入 `application.yaml`；
`workspace --import` 不带文件名时会从 workspace 根目录的 `.yaml` 和 `.yml` 文件中单选。
插件以规范 workspace 作为工作目录运行。请将插件视为可信本地代码，并在导入前检查
它生成的 Policy 候选配置。默认分组列表需要处于 workspace 中；在 workspace 外请使用
`conven plugins --list --global`。

## 运行目录

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

workspace 运行目录和文件仅允许当前用户访问。Conven 会拒绝 symlink 或越界的清理
目标。唯一共享的运行状态是 `~/.conven/state/connections` 下的 connection lease
元数据；业务构建产物、运行时配置和服务日志始终保留在 workspace 中。

## 适用范围

- 服务 runner 不限语言；自动仓库分析覆盖支持矩阵中列出的 Go、Java、Python、Node.js
  和 Bun 框架，未知仓库仍可显式声明为 runner-only。
- 配置可以来自仓库、Apollo 或环境变量，并使用 YAML、properties 或 environment
  Adapter 生成运行契约。
- `ktctl connect` 用于建立本机到集群的访问能力。Conven 不使用 `ktctl exchange`，
  不创建反向路由，也不是 service mesh 或 preview environment 工具。
- 健康检查和 listener/registry observer 只用于证明本次本地启动；Conven 不是监控系统。
- 外部依赖检查只覆盖识别到的绑定，不会完整检查数据库、Kafka、后台任务或所有依赖。

## 帮助与开发

```bash
conven --help
conven help services
man conven
```

安装版本附带的手册是该版本最权威的参考。源码手册位于
[`docs/conven.1`](docs/conven.1)，发布步骤见
[`RELEASING-ZH.md`](RELEASING-ZH.md)，版本变更见 [`CHANGELOG.md`](CHANGELOG.md)。

在项目根目录运行仓库检查：

```bash
bash -n install.sh
go test ./...
go vet ./...
go build ./cmd/conven
```

Conven 使用 [MIT License](LICENSE)。
