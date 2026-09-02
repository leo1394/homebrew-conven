# Service runtime configuration contract

Conven does not require one universal `-f` flag and does not require business
source code to know `CONVEN_CONFIG_DIR`. A typed adapter uses each framework's
native runtime interface, then proves that the process actually honors the
compiled listener and registration contract.

## Contract layers

- **Analyzer:** static language/framework/build/listener/registration and Kafka
  consumer facts.
- **Certifier:** unique policy match and sufficient trust evidence.
- **ConfigDeliveryAdapter:** YAML, properties, or environment delivery.
- **ListenerAdapter:** protected host and port for every listener.
- **RegistrationAdapter:** passive, Kubernetes DNS, Consul, Nacos, Eureka, or
  Etcd behavior.
- **RoutingAdapter:** YAML path, properties key, or environment route.
- **HealthAdapter:** process, TCP, HTTP, or command checks.
- **Observer:** listener ownership and registry snapshot/delta proof.

The orchestrator consumes only the compiled `PlannedRuntime`; it does not branch
on framework names.

## Service requirements

A typed service must:

1. accept its framework-native runtime arguments or environment variables;
2. use the supplied host and port when creating every declared listener;
3. disable registration through a framework-native setting or a neutral,
   default-enabled application setting;
4. guard detected Kafka consumer construction with the neutral,
   default-enabled consumer switch;
5. expose a deterministic build and executable entry;
6. forward process arguments to the framework where arguments are the native
   interface.

Recognizing a flag is not enough: the service must parse and consume it. For
example, declaring `flag.String("f", ...)` without loading that path into the
application config is not a valid runtime contract.

## Native listener interfaces

| Runtime | Protected interface |
|---|---|
| go-zero | `-f <runtime-config-directory>` plus guarded YAML |
| Generic Go/Node/Bun | `HOST` and `PORT`, or `HTTP_PORT`/`RPC_PORT` for multiple listeners |
| Spring Boot | `--spring.config.location`, native server address and port properties |
| Quarkus | `QUARKUS_HTTP_HOST/PORT`, `QUARKUS_GRPC_SERVER_HOST/PORT` |
| Micronaut | `MICRONAUT_SERVER_HOST/PORT`, `GRPC_SERVER_HOST/PORT` |
| Uvicorn | `--host` and `--port` |
| Gunicorn | `--bind host:port` |

Adapters inject protected values last and reject missing, duplicated, or
conflicting values. In loopback mode wildcard addresses fail before startup; in
all-interface mode a non-wildcard listener fails runtime verification.

## Neutral registration switch

The setting belongs to the service, not to Conven. Use a neutral name such as
`SERVICE_REGISTRATION_ENABLED` or `service.registration.enabled`, defaulting to
enabled so existing deployments do not change.

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

The Analyzer must prove that both register and deregister calls are dominated
by the disabled branch. An unrelated environment lookup elsewhere in the file
does not count.

### Spring Boot custom registration

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

`matchIfMissing=true` preserves the existing cloud behavior. Conven supplies
`--service.registration.enabled=false` locally. Standard Spring Cloud
registration uses its native disable property and needs no custom source guard.

### Node.js and Bun

```js
if (process.env.SERVICE_REGISTRATION_ENABLED !== "false") {
  await registerService();
}
```

Bun-native source may use `Bun.env.SERVICE_REGISTRATION_ENABLED`. The register
and deregister calls must be inside the guarded branch.

### Python

```python
enabled = os.getenv("SERVICE_REGISTRATION_ENABLED", "true").lower() != "false"
if enabled:
    register_service()
```

The same condition must protect shutdown deregistration.

## Kafka consumer runtime guard

Typed services that construct Kafka consumers must expose the neutral switch
`SERVICE_KAFKA_CONSUMERS_ENABLED`, defaulting to enabled when it is absent.
`services --registry` records the proven consumer and emits this contract:

```yaml
discovery:
  consumers: [kafka]
isolation:
  consumers:
    kafka:
      mode: guarded
      env: SERVICE_KAFKA_CONSUMERS_ENABLED
```

Conven rescans the selected service before every start or restart, rejects an
unguarded construction path or stale declaration, and currently injects the
default value `true` when the manifest and dependency environments provide no
explicit value. A service environment can explicitly set `false`; values other
than `true` or `false` fail before the process starts. At this stage the
contract proves controllability, not remote consumer-membership isolation.

Go must return before constructing the queue or reader:

```go
if strings.EqualFold(os.Getenv("SERVICE_KAFKA_CONSUMERS_ENABLED"), "false") {
    return []service.Service{}, nil
}
consumer, err := kq.NewQueue(config, handler)
```

Spring Boot can guard the configuration that owns `@KafkaListener` or consumer
construction. Spring relaxed binding maps the environment variable to this
property:

```java
@ConditionalOnProperty(
    prefix = "service.kafka.consumers",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true
)
class KafkaConsumers {
    // listeners or consumer construction
}
```

Node/Bun and Python use the same default-enabled meaning:

```js
if (process.env.SERVICE_KAFKA_CONSUMERS_ENABLED !== "false") {
  await startKafkaConsumers();
}
```

```python
if os.getenv("SERVICE_KAFKA_CONSUMERS_ENABLED", "true").lower() != "false":
    start_kafka_consumers()
```

When explicitly set to `false`, this contract prevents local consumer-group
membership by preventing consumer construction. With the current `true`
default, the consumer is constructed normally. Kafka producers are
intentionally unaffected. Conven does not infer membership from broker TCP
connections because producers and consumers can share the same endpoint. The
default can be reconsidered as `false` after unified local asynchronous-workload
routing is implemented.

## Spring Boot delivery

Spring adapters copy the repository config directory to the runtime tree and
patch only that copy. YAML and `.properties` are supported. Protected command
line values have higher Spring precedence than repository files:

```text
--spring.config.location=file:<runtime-config-directory>/
--spring.profiles.active=<environment>
--server.address=127.0.0.1
--server.port=<declared-http-port>
--grpc.server.address=127.0.0.1
--grpc.server.port=<declared-rpc-port>
```

Registration properties:

- Consul custom guard: `service.registration.enabled=false`
- Spring Cloud Consul: `spring.cloud.consul.discovery.register=false`
- Nacos: `spring.cloud.nacos.discovery.register-enabled=false`
- Eureka: `eureka.client.register-with-eureka=false`

Gradle and Maven support requires the repository wrapper. The Analyzer never
runs a wrapper or accesses the network. A JAR name must be a literal
`archiveFileName`/`finalName`, or derivable from literal project name and
version. Dynamic artifacts, WAR packaging, and ambiguous executable modules
fail closed.

## Environment contract

Environment-based services must read the protected keys when constructing the
actual listener. Examples:

```go
host := os.Getenv("HOST")
port := os.Getenv("PORT")
listener, err := net.Listen("tcp", net.JoinHostPort(host, port))
```

```js
app.listen({ host: process.env.HOST, port: Number(process.env.PORT) })
```

```python
# Conven invokes the framework runner with protected --host/--port or --bind.
# The application object itself remains framework-native.
app = FastAPI()
```

For a multi-listener process, use `HTTP_PORT` and `RPC_PORT`; one service-level
`network.listen` scope applies to every listener.

## Manifest v3 policy example

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

Each declared kind must have a service port, policy server route, and health
check. `routing.servers.<kind>.env` is available alongside `args` and `patches`
for environment contracts.

## Runtime proof

Before startup Conven checks that declared ports are free and snapshots the
selected service's registry identity. After health succeeds, it proves each
listener belongs to the target PID, PGID, or child process and observes the
registry for the configured period. Any unexplained new instance fails closed
and rolls back processes started by the attempt.

`--skip-verify` bypasses health, listener, and registry proof together and is
recorded as `unverified(skip-verify)`.

## Runner-only boundary

A manual `generic-runner` may use arbitrary argv and environment. It must not
declare typed `kinds`, is displayed as runner-only, and receives no typed
listener or registration guarantee. Known frameworks with incomplete evidence
cannot silently use this fallback.
