---
spec: conven-workspace-policy-generator
version: 3
language: zh-CN
repository: "https://github.com/leo1394/homebrew-conven"
pluginInvocation: "conven plugins --run [NAME] [plugin-args...]"
profile: go-zero-apollo-consul-v1
---

# Conven 工作区 Policy 生成器：AI 实现规范

生成器用于维护完整的 Manifest v3 candidate。`.conven/conven.yaml` 是唯一服务清单；
不得读取或重新引入 `.conven/catalog.yaml`、properties 服务清单或硬编码 service preset。

## 输入

1. 当前 `.conven/conven.yaml`：保留 `services` 中 registry 已证明的 path、`kinds`、
   ports、runner、healthChecks、discovery analyzer/certifier/identity、consumer 隔离证据
   和人工配置。
2. `.conven/conven-generator.json`：只保存静态源码无法唯一证明的 `bindingProviders`
   映射；不得保存环境、Policy、service path、kind 或 port。
3. 直接子仓库：只读扫描。不得运行构建、包管理器、网络请求或修改业务源码。

建议输入：

```json
{
  "version": 1,
  "bindingProviders": {
    "partnerRpc": "partner-service"
  }
}
```

凭据只允许引用环境变量，不得写入 JSON、Manifest、日志或错误信息。

## 输出规则

- 输出 UTF-8、单文档、严格字段、完整 `version: 3` candidate。
- `workspace.disabledBindings` 是唯一 disabled binding 集合。
- `discovery.consumerBindings` 表示当前消费者拥有的 binding。
- `discovery.providerAliases` 表示其他消费者引用该 provider 的名称。
- `discovery.consumers` 与 `isolation.consumers` 是 registry 认证的运行时安全事实，必须
  成对保留，不能只生成其中一项。
- binding 到 provider 的多义关系只从 `bindingProviders` 读取，不做大小写归一化猜测。
- 每个 typed `kind` 必须有 port、Policy server route 和 healthChecks。
- 多 listener 使用 `kinds`；`network.listen` 对同一进程所有 listener 生效。
- 生成器不得删除未知但有效的 registry service、runner、端口或 certification。
- 输出前执行完整引用、端口唯一性、依赖、registry 和凭据引用校验；失败时不写目标。

## CLI

插件必须支持：

```text
conven plugins --run [NAME] [plugin-args...]
  --workspace PATH
  --stdout
  --check
  --output [FILE]
  --disable-bindings BINDING...
```

`--stdout` 不写文件；`--check` 比较默认输出；`--output` 不带 FILE 时写
`application.yaml`。显式文件输出可原子替换已有 candidate；写入必须使用同目录临时文件、
fsync 和原子 rename，权限不宽于 `0600`。

生成后工作流：

```bash
conven plugins --run --output
conven workspace --import --edit
conven workspace --validate
conven doctor
conven services --start --dry-run <service...>
```

## 验收

- 重复生成字节一致。
- 输入或认证失败不修改当前 Manifest 和已有 candidate。
- 所有直接子仓库要么保留/生成 service，要么报告具体 skip reason。
- 不包含 secret、kubeconfig 内容、绝对凭据路径或 Conven 专用业务源码变量。
- `conven services --registry` 后再生成时，新 service 的端口、runner 和 certification 保持。
