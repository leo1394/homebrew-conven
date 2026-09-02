# Manifest v3 迁移

Manifest v3 将 typed 信任显式化：`kinds`、逐 listener 健康检查、具名 registry、
registry identity、provider alias、consumer binding 和认证 runtime 都不再等到启动时
猜测。

## 执行迁移

```bash
conven services --stop-all
conven workspace --migrate
conven workspace --validate
conven doctor --dev
conven services --start --dev --dry-run <service...>
```

保存的 service PID 仍在运行时迁移会拒绝执行。命令先在 `.conven/backups` 创建备份，
完整校验 candidate 后再原子替换 `.conven/conven.yaml`。对 v3 重复执行不会重写文件。

迁移完成前，普通 workspace 命令拒绝 v1/v2。`version`、帮助、
`workspace --migrate` 和 `services --stop-all` 仍可用于安全恢复。

## 字段映射

| 旧字段 | v3 结果 |
|---|---|
| `kind` | `kinds: [kind]` |
| `health` | 绑定该 listener 的一项 `healthChecks` |
| `discovery.bindings` | `discovery.consumerBindings` |
| dependency binding alias | 归属可证明时成为 provider 的 `discovery.providerAliases` |
| `environment.registry` | 地址可证明时成为具名 `environment.registries` |
| v2 显式 runner/env service | `generic-runner` runner-only service |

迁移绝不会把 consumer binding 当成 provider alias。如果无法证明 registry identity、
runtime、registry 地址或 alias 归属，整体失败并指出重试前应补充的字段。

## 迁移后检查

- 每个 typed kind 都有 port、Policy server route 和 health check。
- `identity` 是 Consul、Nacos、Eureka 或 Etcd 中的真实名称。
- `providerAliases` 表示消费者如何引用当前 provider；`consumerBindings` 表示当前服务
  拥有的 client。
- 凭据通过 `tokenEnv`、`usernameEnv`、`passwordEnv` 引用环境变量，不写明文。
- 先配置匹配 Policy，再运行 `services --registry`。已识别但无法认证的仓库会使整个更新
  原子失败。
