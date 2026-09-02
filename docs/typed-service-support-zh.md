# Typed service 支持矩阵

只有 Analyzer 能证明仓库事实、Certifier 能选择唯一兼容 Policy，并且 runtime Adapter
能强制每个 listener 和注册规则时，Conven 才授予 typed 信任。

| Runtime/框架 | Listener 证据 | 确定性输入 | 配置/注册 |
|---|---|---|---|
| go-zero | `RestConf` / `RpcServerConf` | `go.mod` + `go.sum` | Apollo/仓库 YAML；Consul |
| Go 标准库、Gin、Echo、Fiber、Chi、grpc-go | 支持的 server 构造以及 `HOST`、`PORT` 消费 | `go.mod` + `go.sum` | environment；passive/Kubernetes DNS/Consul/Nacos/Etcd |
| Kratos、Hertz、Kitex | 框架 server 构造以及 host/port 消费 | `go.mod` + `go.sum` | environment；passive/Consul/Nacos/Etcd |
| Spring Boot | 唯一可执行入口以及 Web/gRPC 证据 | 根目录 `gradlew` 或 `mvnw`；字面量 artifact | YAML/properties；passive/Kubernetes DNS/Consul/Nacos/Eureka |
| Quarkus、Micronaut | HTTP controller/path 或 gRPC 证据 | 根目录 wrapper；字面量 artifact | 框架原生环境变量 |
| FastAPI/Starlette | 唯一 application object | 唯一 Python 锁输入 | Uvicorn 受保护 host/port |
| Flask/Django | 唯一 WSGI application | 唯一 Python 锁输入 | Gunicorn 受保护 bind |
| NestJS | HTTP bootstrap 或 `Transport.GRPC` | 唯一 Node lockfile 生态 | 受保护 environment contract |
| Express/Fastify | 框架 listener bootstrap | 唯一 Node lockfile 生态 | 受保护 `HOST`/`PORT` |
| Bun.serve/Elysia/Hono | Bun listener bootstrap | 仅 Bun lockfile | 受保护 `HOST`/`PORT` |

## Fail-closed 情况

已识别框架存在以下任一情况时，`services --registry` 整体原子失败：

- 无法确定唯一可执行入口或 listener kind；
- lockfile 生态不唯一；
- 构建 artifact 无法静态确定；
- environment contract 无法证明 host/port 被实际消费；
- 自定义注册和注销没有中性、默认开启的 guard；
- 检测到的 Kafka consumer 构造点没有中性、默认开启的 guard；
- 无法匹配唯一 Policy，或某个 kind 缺少 server route。

完全未知的仓库会以具体原因列入 skipped repositories，不阻止其他服务发现。手工声明的
runner-only 仍可运行，但不会显示为 typed。

各语言原生参数、环境变量和注册 guard 示例见
[运行时配置契约](service-runtime-config-contract-zh.md)。
