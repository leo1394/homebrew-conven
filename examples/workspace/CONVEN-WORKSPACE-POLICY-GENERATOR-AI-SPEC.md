---
spec: conven-workspace-policy-generator
specVersion: 1
status: normative-current
language: zh-CN
pluginRuntime: python3
outputFormat: conven-manifest-v2-yaml
pluginInvocation: "conven plugins --run [NAME] [plugin-args...]"
pluginScopes: "workspace-first, optional global"
repository: "https://github.com/leo1394/homebrew-conven"
---

# Conven 工作区 Policy 生成器：AI 实现规范

## 1. 文档用途

本规范用于指导 AI 从零实现一个**与组织无关的 Conven 工作区 Policy 生成器**。
该生成器面向 `go-zero-apollo-consul-v1` profile，并且必须：

1. 只读扫描当前工作区及其直接子仓库；
2. 根据可验证的代码和配置证据识别本地服务、启动命令、端口、RPC binding 与本地依赖；
3. 生成一个可由 Conven 严格解析的完整 `version: 2` candidate manifest；
4. 生成或维护一个可通过 Conven plugin runner 执行的 Python 3 插件；
5. 在证据不足时明确失败或报告 unresolved 项，而不是猜测业务配置。

本文是组织无关的功能规范。生成器代码、默认值、fixture 和示例中不得内置任何
组织名、业务服务名、私有域名、集群名、namespace、凭据路径或固定端口表。candidate
中的工作区专用值只能来自显式输入文件或可定位的 workspace 证据。

本文是自包含规范。实现不得依赖本文未定义的外部脚本、组织知识、隐藏默认值或
预置服务目录；符合性只由本文的规范性要求以及显式输入决定。

本文中的“必须”“不得”“应”“可以”分别表示 MUST、MUST NOT、SHOULD、MAY。

## 2. 产物边界

AI 实现需要区分两个产物：

- **生成器插件**：一个可执行的 Python 3 文件，例如
  `generate-workspace-policy.py`。`conven plugins --run` 执行的是该插件。
- **candidate manifest**：插件扫描工作区后输出的单文档 YAML。该 YAML 需要经过
  `conven policy --import` 才会成为 `.conven/conven.yaml`。

`conven plugins --run` 不负责解析、校验或导入插件生成的 YAML。插件必须自行确保
输出完整、确定且满足本文约束。

### 2.1 输入/输出速查

| 类型 | 名称 | 必需 | 规范 |
| --- | --- | --- | --- |
| 进程输入 | `--workspace PATH` | 是 | Conven 注入的 canonical workspace |
| 文件输入 | `.conven/catalog.yaml` | 是 | repository/RPC identity、kind、local port 和 disabled binding 的显式目录 |
| 文件输入 | `conven-generator.json` | 是 | profile、环境和连接的显式非敏感输入 |
| 只读输入 | 直接子仓库 | 是 | 源码、构建入口、application 和 environment bootstrap |
| CLI 覆盖 | `--disable-bindings` | 否 | 本次运行完整替换 `catalog.yaml` 的 `disabledRpcBindings` |
| 主输出 | Conven manifest v2 YAML | 是 | UTF-8、单文档、严格字段、完整 candidate |
| 诊断输出 | stderr | 按需 | warning、unresolved、fatal；不得混入 `--stdout` YAML |
| AI 实现产物 | 可执行 Python 3 plugin | 是 | 可安装且接受本表所列进程输入 |

运行时成功的最小结果是“candidate YAML 已安全生成”。导入和启动属于后续、显式的
Conven 操作，不是 plugin run 的隐式副作用。

生成器默认只生成 candidate，不得：

- 修改业务仓库源码或仓库内配置；
- 修改或覆盖 `.conven/conven.yaml`；
- 自动执行 `conven policy --import`；
- 访问远程配置中心、注册中心或 Kubernetes；
- 输出、复制或提交凭据；
- 把已有 manifest 当作隐式 merge 基础。

## 3. Conven plugin 执行契约

### 3.1 调用形式

Conven 支持显式名称和缺省名称：

```bash
conven plugins --run NAME [plugin-args...]
```

```bash
conven plugins --run [plugin-args...]
```

插件默认安装到 `<workspace>/.conven/plugins`；`plugins --install --global` 安装到
`~/.conven/plugins`。两层允许同名。显式 `NAME` 优先使用 workspace plugin，本地
不存在时才回退到 global plugin 并 warning；本地同名入口存在但损坏时必须失败，不能
静默回退。`NAME` 缺省时，workspace 恰好存在一个有效 plugin 会直接执行并 warning；
存在多个时打开单选器；workspace 没有 plugin 时从 global 候选中打开单选器，即使只有
一个 global 候选也必须选择并确认。显式 global run 使用
`conven plugins --global --run NAME`，且必须提供 `NAME`。插件自身不得读取或依赖安装名称。

调用示例：

```bash
conven plugins --run generate-workspace-policy \
  --output conven-candidate.yaml

conven plugins --run --output conven-candidate.yaml
```

### 3.2 插件文件要求

插件必须满足：

- 文件扩展名为 `.py`；
- 第一行为 `#!/usr/bin/env python3`，或指向 basename 为 `python3` 的绝对解释器；
- 是真实普通文件，不是符号链接；
- 安装后具有可执行权限；
- 插件名以 ASCII 字母或数字开头，其余字符仅使用 ASCII 字母、数字、`-`、`_`、`.`；
- 优先仅使用 Python 标准库，避免让 Homebrew 用户额外安装依赖。

### 3.3 子进程输入

Conven 执行插件时，等效于：

```text
<selected-plugin-directory>/NAME.py \
  --workspace <canonical-workspace> \
  [plugin-args...]
```

并提供以下运行环境：

- child cwd 是 canonical workspace；
- `CONVEN_WORKSPACE` 被覆盖为同一个 canonical workspace；
- Conven 覆盖任何预先存在的 `CONVEN_WORKSPACE`，并继承父进程的其他环境变量；
- stdin、stdout、stderr 直接透传；
- Conven 总是在用户参数前注入 `--workspace PATH`；
- 用户不能自行传递 `--workspace` 或 `--workspace=...`。

Conven 不为插件提供网络或文件系统沙箱。插件以当前用户权限运行，可能看到父进程
中的 token、代理和云凭据。插件不得枚举、记录、外传或把这些继承变量写入 candidate。

`plugins --run` 先从 effective cwd 向上选择最近的 `.conven` **目录**作为工作区
边界；该目录可以尚未包含 `conven.yaml`。调用前必须位于该工作区内，或使用：

```bash
conven -C <workspace> plugins --run NAME [plugin-args...]
```

用户全局的 `~/.conven` 不是项目工作区边界。

因此插件必须接受且验证：

```text
--workspace PATH
```

插件不得依赖 stdin，唯一例外是交互式输出覆盖确认。

### 3.4 插件退出和输出流

- 成功：插件退出 `0`。
- 参数错误或生成失败：插件退出非零，并把诊断写到 stderr。
- `--stdout` 模式：stdout 只能包含 UTF-8 candidate YAML；warning 和错误必须写 stderr。
- 文件输出模式：stdout 可以输出简短成功摘要；YAML 写入目标文件。
- Conven 会把任意插件非零退出包装为 `conven plugins --run` 失败；不得依赖 Conven
  保留插件自己的具体非零退出码。
- NAME 的选择、scope warning 和候选列表由 Conven runner 输出；插件 stdout 仍必须
  遵守本节约束。

## 4. 生成器 CLI 规范

生成器至少支持：

```text
--workspace PATH
--stdout
--check
--output [FILE]
--disable-bindings BINDING [BINDING ...]
```

### 4.1 参数语义

`--workspace PATH`

- 必填于插件进程接口，由 Conven 注入；
- direct-run 时可以显式传入；
- 必须解析为存在的真实目录；
- 扫描和所有默认相对路径都以该目录为根；
- workspace basename 必须匹配 `[A-Za-z0-9][A-Za-z0-9._-]*`。

`--stdout`

- 把完整 candidate manifest 写到 stdout；
- 不创建或覆盖任何输出文件；
- 不得与 `--output` 或 `--check` 同时使用。

`--check`

- 重新扫描工作区并生成内存结果；
- 与目标文件逐字节比较，可以结合 `--output [FILE]` 选择目标；
- 完全一致退出 `0`；缺失或不同退出非零；
- 不修改目标文件。

`--output FILE`

- 显式目标文件；
- 相对路径以 workspace 为基准；
- 绝对路径也必须解析到 workspace 内，否则拒绝；
- 目标文件存在时，仅 stdin 和 stderr 都是交互终端才允许询问覆盖；
- 只有 `y` 或 `yes`（忽略大小写和首尾空白）表示确认；
- 非交互执行必须拒绝覆盖，不能等待输入。

`--output`（不带 FILE）

- 目标为 `<workspace>/application.yaml`。

既未提供 `--stdout` 也未提供 `--output`

- 默认目标必须为
  `<workspace>/<workspace-name>-conven-candidate.yaml`；
- 不得在默认文件名中写入组织名或固定业务 profile 名。

`--disable-bindings BINDING...`

- 显式禁用一个或多个 RPC binding；
- 可以重复出现，结果去重并稳定排序；
- 一旦显式提供，完整替代 `catalog.yaml` 的 `disabledRpcBindings`，不是追加；
- 不得存在任何代码内置的“永远禁用 binding”。

## 5. 输入模型与证据优先级

输入由以下来源组成，优先级从高到低：

1. 显式 CLI 参数；
2. 人工声明的 workspace 文件，包括 `.conven/catalog.yaml` 和 `conven-generator.json`；
3. workspace 当前文件内容中的构建、入口和运行配置，包括尚未提交的本地修改；
4. 可以由多个独立代码证据一致证明的推导结果。

不得把以下内容作为事实来源：

- 服务名或端口的常识猜测；
- 目录名相似度；
- 未提交的远程注册中心状态；
- AI 对业务架构的先验知识；
- 其他工作区的配置；
- 组织专用的代码常量或默认表。

必要字段无法证明时，生成器必须：

- 给出包含文件、字段和原因的错误；或
- 对规范明确允许忽略的条目输出 warning/unresolved 摘要；
- 不得为了生成“看起来完整”的 YAML 而编造值。

## 6. `.conven/catalog.yaml` 输入规范

### 6.1 位置和用途

固定位置：

```text
<workspace>/.conven/catalog.yaml
```

该文件是服务 identity、kind、本地端口和禁用 RPC binding 的显式规范来源。文件缺失
或无效时，生成器必须失败，并输出 schema 及已发现的候选仓库。实现不得包含内置
服务目录，也不得发明端口或根据目录名隐式选中仓库。

创建服务目录时，AI 可以根据源码和配置证据编辑该文件；端口没有唯一证据时必须
请求输入。支持的 Conven 工作流是先执行 `conven catalog --edit`，再执行
`conven catalog --validate`。插件执行时只消费已经确认的文件，不重新猜测目录内容。

### 6.2 YAML schema

文件必须是 UTF-8、只包含一个 YAML document、拒绝未知字段，并采用以下结构。示例
只表示语法，不代表默认服务：

```yaml
version: 1
services:
  - repository: catalog-api
    kind: http
    port: 18080
  - rpcBinding: catalogRpc
    kind: rpc
    port: 18081
  - repository: inventory-rpc
    rpcBindings: [inventoryRpc, inventoryRPC]
    kind: rpc
    port: 18082
disabledRpcBindings:
  - searchRpc
```

`version` 必须为 `1`。`services` 是有序 sequence。每个 service 必须声明
`repository`、单个 `rpcBinding`、多个大小写敏感的 `rpcBindings`，或组合声明这些字段，
不得隐式合成 identity。`kind` 和 `port` 必填。`disabledRpcBindings` 是可选的 RPC
binding 名称 sequence。

### 6.3 字段约束

字段约束：

- `repository`：匹配 `[A-Za-z0-9][A-Za-z0-9._-]*`；
- `kind`：`go-zero-apollo-consul-v1` profile 只允许 `http` 或 `rpc`；
- `rpcBinding` 和 `rpcBindings` 每一项：首字符为 ASCII 字母或 `_`，其余为 ASCII
  字母、数字、`_`、`-`；
- 声明任一 RPC binding 的 service 必须使用 `kind: rpc`；
- `port`：十进制整数，范围 `1..65535`。

完整目录在过滤 workspace 仓库前必须验证：

- repository 不重复；
- 所有单值和列表 binding 全局不重复，同一 service 内也不得重复；
- port 全局不重复；
- 格式、kind 和字符集合法。

声明顺序是稳定输出顺序。

### 6.4 workspace 子集规则

- 只扫描 workspace 直接子目录，不递归寻找仓库；
- 显式 repository 路径不存在：忽略，允许一份总目录覆盖不同 checkout 子集；
- 显式 repository 路径存在但结构不完整：报错，不得伪装成“仓库未 checkout”；
- workspace 中存在但未在目录声明的仓库：默认忽略；
- 最终没有任何可生成服务：报错。

## 7. 禁用 RPC binding 规则

catalog 的 `disabledRpcBindings` sequence 是持久化的禁用集合。每项使用与
`services[].rpcBinding` 相同的字符约束，并且不得重复。字段缺失或 sequence 为空表示
禁用集合为空。`--disable-bindings` 只在单次生成时于内存中替换该 sequence，不编辑 catalog。

处理规则：

- catalog binding 未在最终服务代码中声明：warning 后忽略；
- CLI binding 未在最终服务代码中声明：报错；
- 命中的 binding 对每个声明它的本地服务生成 service-level config patch；
- 禁用 binding 必须优先于本地依赖推导；
- 禁用 binding 即使指向当前 checkout 中的 provider，也不得生成对应本地 dependency；
- 所有 binding 使用同一套规则，不得按名称写特殊分支。

禁用 patch 的语义形态：

```yaml
config:
  patches:
    - path: <binding>
      value:
        discovType: ""
```

该 patch 只属于第 9 节定义的 profile；`discovType` 不是任意框架的通用字段。若源码
不能证明 client binding 使用该字段关闭发现，则整个 repository 必须 unsupported，
不得改写为其他字段并继续宣称符合该 profile 的 typed isolation。

## 8. `conven-generator.json` 输入规范

固定位置：

```text
<workspace>/conven-generator.json
```

该文件用于外置无法从源码安全推导的环境和连接信息。它必须是 UTF-8 JSON，必须拒绝
重复 key 和未知字段，不得包含 token、证书内容或 kubeconfig 内容。

最小结构：

```json
{
  "version": 1,
  "profile": "go-zero-apollo-consul-v1",
  "policyName": "go-zero-apollo-consul-local",
  "environments": {
    "dev": {
      "registry": "consul",
      "connection": {
        "driver": "none"
      }
    }
  }
}
```

以上环境名和 policy 名仅用于展示结构，不构成生成器默认值。

使用 ktctl 的结构示例：

```json
{
  "version": 1,
  "profile": "go-zero-apollo-consul-v1",
  "policyName": "go-zero-apollo-consul-local",
  "environments": {
    "integration": {
      "registry": "consul",
      "connection": {
        "driver": "ktctl",
        "namespace": "integration",
        "kubeconfigEnv": "CONVEN_KUBECONFIG",
        "sudo": true,
        "timeout": "240s",
        "args": ["--podCreationTimeout", "120"],
        "readiness": [
          {"name": "config", "address": "config.internal:8080"}
        ]
      }
    }
  }
}
```

字段规则：

- `version`：必须为整数 `1`；
- `profile`：必须为 `go-zero-apollo-consul-v1`；
- `policyName`：必须匹配 ASCII `[A-Za-z0-9][A-Za-z0-9._-]*`；
- `environments`：至少一个环境；每个 key 必须匹配 ASCII
  `[A-Za-z0-9][A-Za-z0-9._-]*`，且不得为 `.` 或 `..`；路径分隔符、空白和其他字符
  必须拒绝，因为 key 会进入 `config-<env>.yaml` 文件名；
- `registry`：该 profile 必须为 `consul`；
- `connection.driver`：只允许 `none` 或 `ktctl`；不得输出 `command` 或其他 driver；
- `ktctl` 可以声明 `args`、`kubeconfigEnv`、`context`、`namespace`、`sudo`、
  `timeout`、`readiness`；
- `args` 的每项必须是非空字符串；`context`、`namespace` 必须是非空字符串且不得
  包含控制字符；`kubeconfigEnv` 必须匹配环境变量名 ASCII
  `[A-Za-z_][A-Za-z0-9_]*`；`sudo` 必须是 boolean；`timeout` 必须是正 Go duration
  字符串；
- `readiness` 每项必须具有唯一 `name` 和 `host:port` `address`；`name` 必须匹配
  ASCII `[A-Za-z0-9][A-Za-z0-9._-]*`；
- `ktctl` 的最终 readiness 必须非空，可以由本文件和已验证的 Apollo bootstrap
  endpoint 合并、去重后形成；
- `none` 不得携带 ktctl 专用字段；
- `kubeconfigEnv` 只保存环境变量名称；不得在该文件中保存 kubeconfig 路径或内容。

所有会复制到 manifest 的 JSON 字符串必须通过确定性的 YAML safe encoder 输出，不能
用未转义的字符串拼接 YAML。encoder 必须保持字符串类型并禁止 tag、anchor、alias；
例如 `true`、`null`、`#value` 和包含 `: ` 的字符串不能被解析为其他 YAML 类型。
该 profile 不允许 JSON 连接字段使用 Conven template，因此 `args`、`context`、
`namespace` 和 readiness `address` 中出现字面量 `${` 必须拒绝，而不是留到 plan
阶段报未知变量。

每个 active service 必须存在每个声明环境对应的 bootstrap。仓库中额外的
`config-*.yaml` 不会自动创建环境；需要使用时必须先加入本文件。这样环境、namespace
和连接方式始终是显式输入，不由生成器猜测。

如果没有名为 `dev` 的环境，后续 `doctor` 和 `services --start` 必须显式传
`--env NAME`，因为 Conven 默认选择 `dev`。

## 9. 受支持的 typed profile

### 9.1 支持边界

本规范定义并要求 Conven 强制验证以下 typed profile：

```text
language/build: Go
framework: go-zero
config source: Apollo
discovery: Consul
materializer: yaml-overlay
service kinds: http, rpc
```

该限制来自 Conven 已验证的运行时隔离契约，不是组织规则。

本规范不定义 runner-only、混合技术栈或其他 config source。发现其他语言、框架、
materializer、discovery 或 server kind 时必须标记 unsupported 并停止生成，不得为其
伪造 `go-zero` policy 或 typed isolation。其他技术组合不属于本 profile 的支持范围。

### 9.2 `go-zero-apollo-consul-v1` 仓库布局

该 profile 要求每个显式服务仓库包含：

```text
<repository>/.git             # Git metadata entry; file or directory
<repository>/go/go.mod
<repository>/go/main.go
<repository>/resources/application.yaml
<repository>/resources/config-<env>.yaml
```

布局不是 Conven 的全局规则，但它是本 profile 的固定输入契约。布局不符合时必须
unsupported/fail；不得在同一个 profile 中悄悄混用多个布局。

该 profile 的 Go adapter 必须验证：

- `go.mod` 的 module basename 等于 repository 目录名；
- `main.go` 是 `package main`；
- 入口声明配置目录参数并在初始化远程配置前完成 flag parse；
- 扫描 `go/` 下非测试、非隐藏路径的 `.go` 文件；
- 必须且只能证明一个 server kind：
  - `zrpc.RpcServerConf` => `rpc`；
  - `rest.RestConf` => `http`；
- 检测结果必须与 catalog kind 一致；
- RPC client binding 来自 `zrpc.RpcClientConf` 的 YAML field tag；
- RPC provider key 来自 application 根节点 `consul.key`；
- client target key 来自 `<binding>.consul.key`；
- RPC application 根节点必须存在标量 `discovType` 和 `listenOn`；
- HTTP application 根节点必须存在标量 `host` 和 `port`；若根节点存在
  `discovType`，它也必须是标量且值不得为 `consul`。该 profile 的 adapter 将 HTTP
  registration 视为 `not-applicable`，不会替 HTTP 服务关闭 Consul registration。

实现必须按照 Go 语法结构识别 server kind 和 YAML field tag，不得把注释或字符串
字面量当作证据。可以使用 Go parser 或提供等价保证的分析方式；同时存在相互冲突的
server kind 证据或无法唯一判断 kind 时必须失败。调用辅助分析工具时必须使用 argv、
不得经 shell，并且不能因此修改仓库。

## 10. 环境和配置发现

### 10.1 环境集合

生成器不得固定 `dev`、`test` 或其他环境名。输出环境集合精确等于
`conven-generator.json.environments` 的 key 集合。每个 active service 都必须有每个
环境对应的 `config-<env>.yaml`；缺任一文件即失败。插件不通过 stdin临时补环境。

### 10.2 Apollo bootstrap 证据

每个环境的 bootstrap 至少需要证明：

- `appId` 非空；
- `cluster` 非空；
- `ip` 是 `host:port` 或无 path/query/userinfo 的 `http(s)://host:port`；
- `namespaceName` 必须精确为 `application.yml`。这是
  `go-zero-apollo-consul-v1` profile 的技术契约；其他 Apollo namespace 不属于该
  profile，必须拒绝，不得猜测或静默接受。

同一环境、同一 cluster 出现多个不同 Apollo endpoint 时必须报错。

### 10.3 Connection 和 readiness

不得在代码中固定以下值：

- connection driver；
- Kubernetes context；
- namespace；
- kubeconfig 文件；
- Consul 或 Apollo 域名；
- readiness endpoint。

Connection 精确来自 `conven-generator.json`，仅允许 `none` 或 `ktctl`。使用 `ktctl`
时最终 readiness 必须至少一个；显式 readiness 与 bootstrap endpoint 合并后，名称
冲突但地址不同必须报错。kubeconfig 只通过 `kubeconfigEnv` 引用，不得把凭据内容
写入 manifest。其他 driver 即使在代码中出现，也必须标记 unsupported。

## 11. Binding-only provider 推导

对 `binding:<rpc-binding>,rpc,port`：

1. 在当前 active consumer services 中查找 `<binding>.consul.key`；
2. 没有 key 证据：保持 unresolved，最终忽略；
3. 存在多个不同 target key：报歧义错误；
4. 用唯一 target key 匹配 workspace 候选 RPC repository 的根 `consul.key`；
5. 没有 provider：保持 unresolved，最终忽略；
6. 匹配多个 provider：报歧义错误；
7. 唯一 provider：按完整 repository adapter 严格扫描；
8. 新增 provider 后重新处理其他 unresolved binding，允许链式发现；
9. 如果 binding 推导到一个已经显式配置的 repository：报错，并要求合并为同时声明
   `repository` 和 `rpcBinding` 的单个 entry。

对 `repository,<binding>,rpc,port`：

- repository 存在时严格扫描；
- consumer 有 binding target key 证据时，必须与 provider 根 `consul.key` 一致；
- 多个不同 target key 是歧义错误；
- 无 consumer 证据时可以保留该显式 repository 声明，但应在报告中标明 alias 未验证。

## 12. 本地依赖推导

依赖推导仅在最终 active services 内进行。

算法：

1. 为每个 active RPC provider 建立
   `root consul.key -> service name` 映射；
2. provider key 在 active services 内必须唯一；
3. 对每个 consumer 的每个 client binding，读取其 target `consul.key`；
4. target key 等于某 active provider key时，生成 local dependency；
5. target 不在 active providers 中时，不生成 dependency，由远程路由保留原配置；
6. self dependency 忽略；
7. 同一 consumer 的多个 binding 指向同一 local provider：报错，要求显式消歧；
8. disabled binding 在第 3 步前排除。

依赖输出：

```yaml
dependencies:
  <provider-service>:
    localService: <provider-service>
    binding: <consumer-binding>
    port: rpc
```

`localService` 必须是 candidate manifest 内存在的 service，且 `port` 必须引用目标
service 已声明的 port 名。每个生成的 environment 还必须为该 owner/alias 生成显式
`mode: remote` resolution；当 provider service 同时被本地选择时，Conven 自动以 local
resolution 覆盖它。

## 13. Conven manifest 输出规范

### 13.1 YAML 基本约束

输出必须：

- 是 UTF-8；
- 是单个 YAML 文档；
- 以换行结尾；
- 顶层只包含 `version`、`workspace`、`environments`、`policies`、`services`；
- 不包含未知字段；
- `version` 精确为整数 `2`；
- `workspace.name` 非空；
- 至少包含一个 service；
- 不使用 YAML merge key、隐式业务默认或多文档分隔；
- 对同一输入产生逐字节稳定的输出。

service、policy 和环境名必须匹配 ASCII
`[A-Za-z0-9][A-Za-z0-9._-]*`；环境名还不得为 `.` 或 `..`。binding 必须匹配 ASCII
`[A-Za-z_][A-Za-z0-9_-]*`。kind 和 port 名必须非空且不含任何 Unicode whitespace；
该 profile 生成的 kind 和 port 名还必须使用本文定义的固定枚举。

### 13.2 顶层结构

逻辑结构：

```yaml
version: 2

workspace:
  name: <workspace-name>
  policy: <default-policy-name>

environments:
  <environment-name>:
    registry: <registry-name>
    # connection only when proven or explicitly configured

policies:
  <default-policy-name>:
    drivers: {}
    config: {}
    process: {}
    routing: {}

services:
  <service-name>: {}
```

`workspace.policy` 如果存在，必须引用 `policies` 中的实际 key。service 可以通过
`service.policy` 覆盖 workspace 默认，但引用也必须存在。

### 13.3 `go-zero-apollo-consul-v1` typed policy

仅当源码证据满足第 9 节 profile 时，生成如下语义的 policy。实际 policy key 使用
`conven-generator.json.policyName`；下例名称只是组织无关示例：

```yaml
policies:
  go-zero-consul-local:
    drivers:
      framework: go-zero
      configSource: apollo
      discovery: consul
      materializer: yaml-overlay
    config:
      sourceDir: resources
      application: application.yaml
      bootstrap: config-${env}.yaml
      runtimeBootstrap: config-local.yaml
      apollo:
        attempts: 5
        retryDelay: 2s
        timeout: 15s
      patches:
        - file: config-local.yaml
          path: localConfigEnable
          value: true
        - file: config-local.yaml
          path: localConfigPath
          value: ${configDir}/application.yaml
    process:
      env:
        PROFILE_ACTIVE: local
      args: [-f, "${configDir}"]
    routing:
      servers:
        rpc:
          port: rpc
          isolation:
            registration:
              mode: config
              path: discovType
              disabledValue: ""
            listener:
              path: listenOn
              value: "127.0.0.1:${port.rpc}"
        http:
          port: http
          patches:
            - path: port
              value: "${port.http}"
          isolation:
            registration:
              mode: not-applicable
            listener:
              path: host
              value: "127.0.0.1"
      localDependency:
        mode: replace
        value:
          target: "127.0.0.1:${dependency.port}"
      remoteDependency:
        mode: preserve
```

以上是 `go-zero-apollo-consul-v1` profile 的精确契约，不是所有 go-zero 项目的通用约定。
生成前必须验证目标项目确实使用 `resources`、`config-local.yaml`、`-f`、
`PROFILE_ACTIVE=local`、`localConfigEnable`、`localConfigPath`、`discovType`、
`listenOn`、`host` 和 `port` 这些路径及语义。任何一项不成立都必须 unsupported/fail；
不得生成本文未定义的 policy 变体并宣称符合该 profile。

### 13.4 Service 输出

该 profile 的 Go adapter 输出以下 service 形态：

```yaml
services:
  <service-name>:
    path: <repository-relative-path>
    kind: <http-or-rpc>
    runner:
      workdir: go
      build: [go, build, -o, "${artifact}", .]
      run: ["${artifact}"]
    ports:
      <kind>: <validated-port>
    health:
      type: tcp
      address: "127.0.0.1:${port.<kind>}"
```

要求：

- `path` 必须是 workspace 内实际存在的相对路径；
- `runner.run` 必填；
- command 使用 argv 数组，不通过 shell；
- command 的每个元素非空；
- port 范围 `1..65535`；
- typed service 的 kind 必须在 policy routing servers 中存在；
- kind 对应的 service port 必须存在；
- health 地址必须使用 loopback 和该 service 的已声明端口。
- 生成的新 service 必须省略 `network.listen`，即默认 loopback；更新时必须保留已有的
  合法 `network.listen`，且没有人工明确决定时不得自动设为 `all-interfaces`。

### 13.5 模板变量

只使用 Conven manifest v2 支持的变量：

```text
${workspace}
${service}
${serviceDir}
${stateDir}
${runDir}
${configDir}
${artifact}
${env}
${port.NAME}
${services.NAME.ports.PORT}
${dependency.name}
${dependency.port}
```

最后两个只用于 dependency route value。不得发明新的模板变量，也不得假设所有 YAML
字段都会自动插值。

## 14. 静态不变量与运行期假设

“YAML 可以 import”不等于“服务可以 start”。生成器必须离线检查以下静态不变量：

- service path 和 runner workdir 实际存在；
- runner run 可执行语义完整；
- policy `sourceDir` 是非 `.`、非 `..`、不能逃逸的相对子目录；
- policy `application` 是非 `.`、不能逃逸的相对文件；
- `yaml-overlay` 搭配 `configSource: apollo`；
- Apollo source 同时声明 bootstrap 和 runtimeBootstrap；
- patch path 是非空点分路径，value 非 null；
- route mode 只使用 `preserve` 或 `replace`；
- replace route 有 value；
- RPC registration isolation 使用 config mode；
- Policy 中 RPC listener 基线是 `127.0.0.1:<declared-rpc-port>`；只有 service 已明确保留
  `network.listen: all-interfaces` 时，Conven 才能将运行时副本覆盖为
  `0.0.0.0:<declared-rpc-port>`；
- HTTP registration isolation 是 `not-applicable`；
- Policy 中 HTTP listener host 基线是不带端口的 loopback IP；只有 service 已明确保留
  `network.listen: all-interfaces` 时，Conven 才能将运行时副本覆盖为 `0.0.0.0`；
- typed go-zero process 只附加一个 `-f <absolute-runtime-config-dir>`；
- runtime bootstrap 精确为 `config-local.yaml`，最终 `PROFILE_ACTIVE` 精确为 `local`；
- local dependency 有 policy local route 或显式 localEnv；
- 未选中的 dependency 通过 environment 中显式的 `mode: remote` resolution 保留远程配置；
- 不生成指向 manifest 外 service 的 dependency；
- 不生成 self dependency。

以下项目是离线插件无法证明的运行期假设，必须出现在成功报告中，不能声称已验证：

- Apollo endpoint 在实际网络中可达，并允许该 profile 的 Apollo adapter 发起未认证的
  `GET /configs/...` 请求；本 profile 没有 Apollo credential/header 输入字段；
- Apollo 返回的最终 application 与 repository evidence 一致；
- 最终 application 根节点的 RPC `discovType`/`listenOn` 或 HTTP `host` 仍存在且为
  标量；HTTP 根 `discovType` 不得为 `consul`；
- ktctl、Kubernetes context、namespace 和 readiness 在目标机器有效；
- 未本地启动的 Consul dependency 有 passing instance；
- build、process、health check 和实际端口监听成功。

验收结果必须分级：

- **A / import-valid**：YAML 通过 Conven 严格 schema；
- **B / plan-valid(environment, service-selection)**：指定环境和本地服务集合的
  `doctor` 与 `services --start --dry-run` 通过本地静态计划。一次通过不能代表其他
  环境或其他服务组合；报告必须列出实际验证的 tuple。只有全部声明环境和文档承诺的
  service-selection 矩阵都通过时，才可以将整个 candidate 简称为 plan-valid；
- **C / start-validated**：真实 materialize、连接、build、start、health 和远程依赖预检
  均通过。C 需要用户显式授权及网络环境，生成器默认不得执行，也不得报告为已完成。

## 15. 确定性和安全写入

### 15.1 稳定顺序

- services 按 `.conven/catalog.yaml` 声明顺序；
- 推导服务按触发其 binding 条目的顺序；
- disabled bindings 按名称排序；
- dependency 目标按最终 service 顺序；
- environments 和 readiness 使用稳定排序；
- map 输出顺序必须显式控制，不能依赖不稳定遍历。

### 15.2 写入协议

- 所有扫描和验证先完成，再开始写文件；
- 在目标同目录创建临时文件；
- 写入、flush、fsync 后原子发布；
- 新文件采用 no-clobber；
- 覆盖只能在交互确认后使用原子 replace；
- 输出文件权限建议为 `0644`；
- 失败时删除临时文件且保留原目标不变。

### 15.3 路径安全

- canonicalize workspace；每个 service path 的 realpath 必须仍在 workspace 内；
- service path、读取的 bootstrap/application 和 config source tree 不得是 symlink；
- config source tree 内出现任何 symlink 必须失败；`.git` metadata entry 不受此规则影响；
- 显式 output 的规范路径必须仍在 workspace 内，且不得指向 symlink；
- 不扫描 workspace 之外的路径；
- 不执行从仓库内容拼接出的 shell 字符串；
- 不在日志中打印完整环境变量、token、kubeconfig 内容或远程配置响应。

## 16. 错误与 warning 分类

必须失败的情况包括：

- workspace 无效；
- catalog YAML 或 schema 非法；
- 重复 repository、binding 或 port；
- 显式存在的 repository 结构不完整；
- catalog kind 与源码 kind 不一致；
- active RPC provider key 重复；
- binding 对应多个 target key 或多个 provider；
- 显式 CLI disabled binding 未在代码中声明；
- 生成结果无法满足 Conven v1 schema；
- 输出存在但无法安全确认覆盖。

以下情况必须 warning 后继续：

- catalog repository 没有 checkout；
- binding-only 缺少 consumer key 证据；
- binding-only 有唯一 key但本地没有 provider；
- `disabledRpcBindings` 中的 binding 未在最终 active services 中声明；
- dual-identity entry 缺少 consumer 证据，因而 RPC alias 未验证。

以上 warning 必须包含来源文件和行号；不得静默忽略，也不得输出 secret value。

建议采用稳定诊断格式：

```text
warning: <source>:<line>: <code>: <message>
generation failed: <source-or-stage>: <message>
```

无法对应文件行号的 workspace/runtime 诊断使用明确 stage，例如 `workspace`、
`discovery`、`render` 或 `output`。诊断格式不得要求调用者解析 ANSI 颜色。

## 17. 验收流程

### 17.1 插件实现检查

先定义绝对路径，并确认当前 workspace 已有 `.conven` 工作区边界：

```bash
workspace="$(pwd -P)"
: "${PLUGIN_SOURCE:?set PLUGIN_SOURCE to the absolute generator plugin path}"
plugin_source="$PLUGIN_SOURCE"
plugin_name="$(basename "$plugin_source" .py)"
test -d "$workspace/.conven"
test "${plugin_source#/}" != "$plugin_source"
test_dir="$(mktemp -d "$workspace/.conven/generator-test.XXXXXX")"
candidate_stdout="$test_dir/from-stdout.yaml"

PYTHONPYCACHEPREFIX="$test_dir/pycache" \
  python3 -m py_compile "$plugin_source"
python3 "$plugin_source" --workspace "$workspace" --stdout \
  > "$candidate_stdout"
```

唯一的 test directory 保证 shell 不会截断已有文件。确认 stdout 文件只包含 YAML，
诊断只出现在 stderr。

### 17.2 Conven plugin 检查

```bash
candidate_named="$test_dir/from-explicit-name.yaml"

# Default installation is workspace-local and may ask before replacing a same-name copy.
conven -C "$workspace" plugins --install "$plugin_source"
conven -C "$workspace" plugins --list

conven -C "$workspace" plugins --run \
  "$plugin_name" --output "$candidate_named"

cmp "$candidate_stdout" "$candidate_named"
```

当该 workspace 中本插件是唯一有效的 workspace plugin 时，还必须验证缺省 NAME：

```bash
candidate_default="$test_dir/from-default-plugin.yaml"
conven -C "$workspace" plugins --run \
  --output "$candidate_default"
cmp "$candidate_named" "$candidate_default"
```

若 workspace 中存在多个 plugin，该检查必须在只安装本插件的一次性 workspace fixture
执行；不得删除用户的其他 plugin 来满足测试。插件收到的 `--workspace` 和 plugin args
语义在显式/缺省 NAME 下必须相同。

### 17.3 Manifest 严格校验

不要在未审查前替换真实 manifest。可以在一次性 workspace 中验证 import：

```bash
validation_root="$(mktemp -d)"
mkdir -p "$validation_root/.conven"
conven -C "$validation_root" policy --import \
  "$candidate_named"
```

这一步只证明 A / import-valid，不证明 candidate 中的 service path 在临时 workspace
可运行。不得在报告中把它升级为 plan-valid 或 start-validated。

人工检查 candidate 并明确同意替换后，真实导入使用本次实际生成的文件：

```bash
conven policy --import "$candidate_named"
```

`policy --import` 是完整替换，不是 merge。`--edit` 会进入交互 editor，只能作为可选的
人工发布步骤，不属于可重复的自动验收。

随后对**每个** `conven-generator.json` 声明的环境和每个承诺支持的本地服务集合分别
执行以下命令模板，并在报告中记录准确 tuple；不得只验证一个环境后声称整个 candidate
达到 B：

```bash
conven doctor --env ENVIRONMENT
conven services --start --env ENVIRONMENT --dry-run SERVICE...
```

### 17.4 必须覆盖的回归场景

- catalog 缺失、YAML 无效、多 document、未知字段、services 为空；
- generator JSON 缺失、未知字段、非法 profile、非法 connection driver；
- 环境名中的 `.`、`..`、路径分隔符、空白或非 ASCII 安全字符；
- JSON 连接字符串中的 YAML 特殊值、控制字符和禁止的 `${...}`；
- 注释前有空白；
- 目录声明多于 checkout 仓库；
- 显式存在但不完整的 repository；
- 未声明的 workspace repository；
- binding-only 正向推导；
- 无 key、无 provider、多 key、多 provider；
- dual-identity entry 匹配、不匹配、无证据；
- HTTP 根 `discovType: consul` 必须拒绝；
- `disabledRpcBindings` 为空、非法或重复；
- CLI disabled 在内存中完整覆盖 catalog sequence；
- disabled binding 指向 active provider 时仍不建立 local dependency；
- `--stdout`、`--check`、新文件、交互覆盖和非交互拒绝覆盖；
- 输出 YAML 单文档、无未知字段、可被 Conven import；
- 相同输入重复运行产生逐字节一致输出。

## 18. AI 禁止事项

AI 不得：

- 内置真实服务名或业务 binding 名；
- 内置组织 policy 名；
- 固定 `dev/test`、namespace、cluster、Consul/Apollo 地址或 kubeconfig；
- 猜端口、依赖、服务 kind 或运行命令；
- 把任意 Go 仓库都当作 go-zero 服务；
- 把任意框架伪装为 `go-zero-apollo-consul-v1` profile；
- 为了通过 schema 而生成指向不存在服务或端口的 dependency；
- 自动修改业务 `application.yaml`；
- 自动导入 candidate；
- 在失败后留下半写入文件；
- 把 warning 混入 `--stdout` 的 YAML；
- 读取 stdin 导致非交互运行阻塞；
- 访问网络来补全无法从 workspace 证明的信息。

## 19. 可直接交给 AI 的任务摘要

以下区块可以与本规范全文一起提供给实现 AI：

```text
你需要为当前 workspace 实现一个组织无关的 Conven Policy 生成器插件。

先只读扫描 workspace 及直接子仓库，记录每个结论对应的文件证据。实现一个
Python 3 可执行 .py 插件；它必须接受 Conven 注入的 --workspace PATH，并支持
--stdout、--check、--output [FILE]、--disable-bindings。

插件需要输出完整、确定、单文档、严格字段的 Conven version: 2 candidate manifest。
不得修改源码或 .conven/conven.yaml，不得访问网络，不得输出凭据。生成器代码、
默认值、fixture 和示例不得内置组织名、真实业务服务名、私有地址、环境名、namespace
或端口表；candidate 中出现的工作区专用值必须来自显式输入或可定位的代码证据。

读取 .conven/catalog.yaml 和 conven-generator.json，
并严格遵守本规范的语法、子集、binding-only、禁用、环境和依赖推导规则。所有无法
从代码或显式输入证明的端口、业务依赖、环境连接和远程地址都不得猜测；请报错或
列为 unresolved。

只有源码满足第 9 节定义的 Go + go-zero + Apollo + Consul + yaml-overlay typed
isolation 精确契约时，才能生成带 kind 的 http/rpc service 和对应 policy。其他技术栈
必须标记 unsupported；本规范不允许退化为 runner-only service。

生成后执行语法检查、确定性检查、临时文件输出检查和一次性 workspace import
校验。不要在真实 workspace 自动执行 policy --import。最终报告生成文件、证据、
未解析项、验证命令和验证结果。

Conven 使用 conven plugins --run [NAME] [args...]。插件不得依赖名称选择方式，
显式和缺省 NAME 调用必须接收相同的 --workspace 和 plugin args 契约。
```

## 20. 完成定义

只有同时满足以下条件才算完成：

- 插件能被 `conven plugins --install` 接受；
- 显式 NAME 调用能正常运行；
- 插件逻辑不依赖自身 NAME，唯一 workspace plugin 时缺省 NAME 调用也能正常运行；
- candidate 可被 Conven v1 严格解析；
- candidate 中的工作区专用值均可追溯到显式输入或已验证的 workspace 证据；
- 所有 service、port、runner、binding、dependency、environment 和 connection 都有
  workspace 证据或显式输入；
- disabled binding 在 patch 和 dependency 两处语义一致；
- 输出确定、可复现、安全写入；
- 未经用户确认不覆盖现有文件，不导入真实 manifest；
- 验收测试和 unresolved 报告完整。
