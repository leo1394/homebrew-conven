# Service 运行时配置契约

Conven 不要求所有语言统一使用 `-f`，也不要求业务源码识别 `CONVEN_CONFIG_DIR`。Typed
Adapter 使用框架原生运行时接口，并在启动后证明进程确实遵守编译后的 listener 和注册
契约。

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
4. 用中性、默认开启的 consumer 开关保护检测到的 Kafka consumer 构造点；
5. 提供确定性的构建和可执行入口；
6. 以参数作为原生接口时，把进程 argv 继续交给框架。

仅“识别参数”不够。例如 Go 代码声明了 `flag.String("f", ...)`，却没有用该路径加载
应用配置，不构成有效运行时契约。

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

## Kafka consumer runtime guard

创建 Kafka consumer 的 typed service 必须支持中性开关
`SERVICE_KAFKA_CONSUMERS_ENABLED`；环境变量缺失时默认开启。
`services --registry` 证明构造路径受保护后生成：

```yaml
discovery:
  consumers: [kafka]
isolation:
  consumers:
    kafka:
      mode: guarded
      env: SERVICE_KAFKA_CONSUMERS_ENABLED
```

Conven 每次 start/restart 前重新扫描所选 service，拒绝未受保护的构造路径和过期声明；
合并 manifest 与依赖环境变量后，如果没有显式值，当前阶段注入默认值 `true`。service
环境可以显式设置 `false` 临时关闭 consumer；非 `true`/`false` 值会在进程启动前失败。
该 contract 当前只证明 consumer 可控，不构成远程 consumer membership 隔离保证。

Go 必须在创建 queue/reader 前返回：

```go
if strings.EqualFold(os.Getenv("SERVICE_KAFKA_CONSUMERS_ENABLED"), "false") {
    return []service.Service{}, nil
}
consumer, err := kq.NewQueue(config, handler)
```

Spring Boot 可保护持有 `@KafkaListener` 或 consumer 构造逻辑的 configuration；Spring
relaxed binding 会把环境变量映射到以下属性：

```java
@ConditionalOnProperty(
    prefix = "service.kafka.consumers",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true
)
class KafkaConsumers {
    // listener 或 consumer 构造逻辑
}
```

Node/Bun 与 Python 使用相同的默认开启语义：

```js
if (process.env.SERVICE_KAFKA_CONSUMERS_ENABLED !== "false") {
  await startKafkaConsumers();
}
```

```python
if os.getenv("SERVICE_KAFKA_CONSUMERS_ENABLED", "true").lower() != "false":
    start_kafka_consumers()
```

显式设置 `false` 时，该契约通过不创建 consumer 阻止本地进程加入远程 consumer group；
默认 `true` 时 consumer 正常创建。Kafka producer 不受影响。Conven 不从 broker TCP 连接
推断 membership，因为 producer 和 consumer 可以共用同一 endpoint。统一异步工作负载
本地路由实现后，再评估把默认值收紧为 `false`。

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

`--skip-verify` 会一起跳过健康、listener 和 registry 证明，状态记录为
`unverified(skip-verify)`。

## Runner-only 边界

手工 `generic-runner` 可以使用任意 argv 和环境变量，但不能声明 typed `kinds`，status
显示 runner-only，且没有 listener/注册保证。已知框架证据不足时不能静默使用该回退。
