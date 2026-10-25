# Service 运行时配置契约

Conven 不要求所有语言统一使用 `-f`，也不要求业务源码识别 `CONVEN_CONFIG_DIR`。Typed
Adapter 使用框架原生运行时接口，并在启动后证明进程确实遵守编译后的 listener 和注册
契约。

## Apollo RPC binding 仓库回退

Manifest v3 的 Go/go-zero + Apollo + yaml-overlay 默认启用
`policy.config.bindingFallback.mode: repository-if-missing`（也可设为 `disabled`）。
修改配置指定的仓库 application YAML 后执行 `conven services --update` 同步拓扑，
下次启动或重启重新读取文件，无需在 manifest 中重复填写连接参数。

- 仅处理源码 `RpcClientConf` 声明与清单 consumer/dependency binding 的交集。
- Apollo 完全没有该键时，才补入仓库中有效的 RPC 子树；不复制数据库等无关字段。
- Apollo 已有值、`null`、空 mapping 不触发回退；无效路由报错，不用仓库值掩盖。
- 本地替换和 disabled binding 不参与回退。显式 patches 和隔离 guards 仍随后执行。
- 支持经过现有 RPC 校验器验证的 Consul、target、Etcd、源码确认拼写的 endpoints。
- Apollo 请求失败仍失败；回退不是离线配置模式。来源提示只打印 binding 名和来源。
- 配置文件须为真实文件；重复键、merge key、循环 alias 和多文档 YAML 拒绝处理。

`--update` 不访问 Apollo、不生成配置 patches、不保存连接凭据。Consul identity 改变时，
旧 provider 依赖及其环境 resolution 被移除，唯一匹配的新 provider 默认走远程；没有
匹配则保留为远程配置 binding，不猜测本地 provider。没有 identity 的既有显式映射保留。
注释仓库 binding 只清理 Conven 拓扑，不删除 Apollo 自身已有的配置；要禁用该客户端，
仍应使用 `services --disable-binding` 并满足源码禁用契约。

## 契约分层

- **Analyzer**：静态语言、框架、构建、listener、注册和 Kafka consumer 事实。
- **Certifier**：唯一 Policy 匹配和充分信任证据。
- **ConfigDeliveryAdapter**：YAML、properties 或 environment 配置交付。
- **ListenerAdapter**：为每个 listener 生成受保护 host/port。
- **RegistrationAdapter**：passive、Kubernetes DNS、Consul、Nacos、Eureka、Etcd。
- **RoutingAdapter**：YAML path、properties key 或环境变量路由。
- **HealthAdapter**：process、TCP、HTTP、command 检查。
- **Observer**：listener 进程归属和 registry snapshot/delta 证明。

Orchestrator 只消费编译后的 `PlannedRuntime`，不判断具体框架名。

## Service 必须满足

Typed service 必须：

1. 接收框架原生运行时参数或环境变量；
2. 创建每个已声明 listener 时真正使用传入 host 和 port；
3. 通过框架原生设置或中性、默认开启的应用设置禁用注册；
4. 用可验证的环境变量开关或 Spring Boot 属性保护检测到的 Kafka consumer 构造点；
5. 提供确定性的构建和可执行入口；
6. 以参数作为原生接口时，把进程 argv 继续交给框架。

仅“识别参数”不够。例如 Go/go-zero 必须在读取 `flag.String("f", ...)` 返回值之前调用
`flag.Parse()`，再从该路径加载应用配置；缺少任一步都不构成有效运行时契约。

## 框架原生 listener 接口

| Runtime | 受保护接口 |
|---|---|
| go-zero | `-f <runtime-config-directory>` 和 guarded YAML |
| 通用 Go/Node/Bun | `HOST`、`PORT`；多 listener 使用 `HTTP_PORT`/`RPC_PORT` |
| Spring Boot | `--spring.config.location` 和原生 server address/port 属性 |
| Quarkus | `QUARKUS_HTTP_HOST/PORT`、`QUARKUS_GRPC_SERVER_HOST/PORT` |
| Micronaut | `MICRONAUT_SERVER_HOST/PORT`、`GRPC_SERVER_HOST/PORT` |
| Uvicorn | `--host`、`--port` |
| Gunicorn | `--bind host:port` |

Adapter 最后注入受保护值，并拒绝缺失、重复或冲突。loopback 模式在启动前拒绝通配
地址；all-interfaces 模式在运行时拒绝实际未监听通配地址的进程。

## 中性注册开关

设置属于 service，而不是 Conven。使用 `SERVICE_REGISTRATION_ENABLED` 或
`service.registration.enabled` 这样的中性名称，并默认开启，确保原部署行为不变。

### Go

```go
enabled := !strings.EqualFold(os.Getenv("SERVICE_REGISTRATION_ENABLED"), "false")
if enabled {
    registerService()
}
defer func() {
    if enabled {
        deregisterService()
    }
}()
```

Analyzer 必须证明 register 和 deregister 都受禁用分支支配。在文件其他位置读取环境变量
不能作为证明。

### Spring Boot 自定义注册

```java
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;

@Configuration
@ConditionalOnProperty(
    prefix = "service.registration",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true
)
public class ConsulConfig {
    // registration and deregistration
}
```

`matchIfMissing=true` 保持原云端行为；Conven 本地传入
`--service.registration.enabled=false`。标准 Spring Cloud 注册使用框架原生禁用属性，
不需要修改源码。

### Node.js 和 Bun

```js
if (process.env.SERVICE_REGISTRATION_ENABLED !== "false") {
  await registerService();
}
```

Bun 原生代码可使用 `Bun.env.SERVICE_REGISTRATION_ENABLED`。注册和注销调用都必须在
guard 分支内部。

### Python

```python
enabled = os.getenv("SERVICE_REGISTRATION_ENABLED", "true").lower() != "false"
if enabled:
    register_service()
```

关闭阶段的注销也必须受同一条件保护。

## Kafka 消费与 CLI 边界

普通 Conven CLI 启动不隔离 Kafka 消费，本地服务可能加入远程共享消费组。
CLI 不解释 `SERVICE_KAFKA_CONSUMERS_ENABLED`，不要求源码 guard，也不注入消费
环境变量或属性开关。继承的环境变量和显式业务配置保留原有语义，不强制开启消费者。
旧的 `isolation.consumers` 声明继续作为可读取的元数据，但不在 CLI 启动时执行
消费隔离；计划将已声明消费者显示为 unrestricted。消费能力发现仅供提示，
不等于完整的 broker membership 检测。Guard 网络防护是独立、需验证的边界。
Conven 不修改业务源码、不创建隔离 topic/group，也不修改集群 ACL。

## Spring Boot 配置交付

Spring Adapter 把仓库配置目录复制到 runtime，只改运行副本。支持 YAML 和
`.properties`。受保护命令行值在 Spring 中优先级高于仓库文件：

```text
--spring.config.location=file:<runtime-config-directory>/
--spring.profiles.active=<environment>
--server.address=127.0.0.1
--server.port=<declared-http-port>
--grpc.server.address=127.0.0.1
--grpc.server.port=<declared-rpc-port>
```

注册属性：

- Consul 自定义 guard：`service.registration.enabled=false`
- Spring Cloud Consul：`spring.cloud.consul.discovery.register=false`
- Nacos：`spring.cloud.nacos.discovery.register-enabled=false`
- Eureka：`eureka.client.register-with-eureka=false`

Gradle/Maven 只接受仓库 wrapper。Analyzer 不运行 wrapper、不访问网络。JAR 名必须是
字面量 `archiveFileName`/`finalName`，或能由字面量项目名和版本推导。动态 artifact、
WAR 和多义可执行模块均 fail-closed。

## Environment contract

Environment 类型 service 必须在创建真实 listener 时读取受保护 key：

```go
host := os.Getenv("HOST")
port := os.Getenv("PORT")
listener, err := net.Listen("tcp", net.JoinHostPort(host, port))
```

```js
app.listen({ host: process.env.HOST, port: Number(process.env.PORT) })
```

```python
# Conven 使用受保护的 --host/--port 或 --bind 调用框架 runner。
# application object 保持框架原生。
app = FastAPI()
```

同一进程有多个 listener 时使用 `HTTP_PORT` 和 `RPC_PORT`；service 级
`network.listen` 对全部 listener 生效。

## Manifest v3 Policy 示例

```yaml
policies:
  spring-boot-nacos:
    drivers:
      runtime: spring-boot
      framework: spring-boot
      configSource: repository
      discovery: nacos
      materializer: yaml-overlay
    config:
      sourceDir: src/main/resources
      application: application.yml
    process:
      args:
        - "--spring.config.location=file:${configDir}/"
        - "--spring.cloud.nacos.discovery.register-enabled=false"
    routing:
      servers:
        http:
          port: http
          patches:
            - path: server.port
              value: "${port.http}"
          args:
            - "--server.address=127.0.0.1"
            - "--server.port=${port.http}"
          isolation:
            registration:
              mode: config
              path: spring.cloud.nacos.discovery.register-enabled
              disabledValue: false
            listener:
              path: server.address
              value: 127.0.0.1
```

每个 kind 都必须有 service port、Policy server route 和 health check。
`routing.servers.<kind>.env` 可与 `args`、`patches` 并列用于 environment contract。

## 运行时证明

启动前 Conven 检查端口未占用，并读取所选 service registry identity 的快照。健康检查
通过后，证明每个 listener 属于目标 PID、PGID 或子进程，再持续观察 registry。任何
无法解释的新增实例都会 fail-closed，并回滚本次启动的进程。

注册中心读取遇到临时超时、EOF、连接中断或 HTTP 429/502/503/504 时，最多尝试
4 次，依次退避 250ms、500ms、1s；单次请求仍限时 5 秒。恢复后重新完成整个观察
窗口，并始终与启动前的原始快照比较，失败请求不计入隔离证明。观察总时限为配置的
观察时长加 30 秒。权限错误、无效响应和新增实例仍然失败关闭；持续不可达时回滚
本次启动，不删除注册中心数据。

`--skip-verify` 会一起跳过健康、listener 和 registry 证明，状态记录为
`unverified(skip-verify)`。

## Runner-only 边界

手工 `generic-runner` 可以使用任意 argv 和环境变量，但不能声明 typed `kinds`，status
显示 runner-only，且没有 listener/注册保证。已知框架证据不足时不能静默使用该回退。
