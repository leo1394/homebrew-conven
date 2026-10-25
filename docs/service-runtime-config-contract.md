# Service runtime configuration contract

Conven does not require one universal `-f` flag and does not require business
source code to know `CONVEN_CONFIG_DIR`. A typed adapter uses each framework's
native runtime interface, then proves that the process actually honors the
compiled listener and registration contract.

## Apollo RPC binding repository fallback

Manifest v3 Go/go-zero + Apollo + yaml-overlay policies default to
`config.bindingFallback.mode: repository-if-missing`; set `disabled` to opt out.
After editing the configured repository application YAML, run
`conven services --update` to synchronize topology. The next start or restart
reads the file afresh without duplicating connection values in the manifest.

- Candidates must be both source-declared `RpcClientConf` fields and manifest
  consumer/dependency bindings. Only the RPC subtree is copied, never unrelated settings.
- Only an absent Apollo key permits fallback. Existing values, nulls and empty
  mappings retain precedence; invalid routes fail instead of being masked.
- Local replacements and disabled bindings are excluded. Explicit patches and
  isolation guards still run afterward.
- Consul, target, Etcd and source-proven endpoint spellings use the same RPC validator.
- Apollo fetch failures remain failures, not an offline mode. Diagnostics report
  binding names and origins, not connection values or credentials.
- Real files are required; duplicate/merge keys, cyclic aliases and multiple YAML
  documents are rejected.

Update does not contact Apollo or generate configuration patches. A changed Consul
identity removes the old provider dependency and its environment resolution; a
unique new provider defaults to remote. Unknown identities remain remote-only
configuration bindings. Existing explicit mappings without an identity are kept.
Commenting a repository binding removes its Conven topology, not an existing
Apollo value. Use `services --disable-binding` and the source disable contract
to disable that client.

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
4. guard detected Kafka consumer construction with a verifiable environment
   switch or Spring Boot property;
5. expose a deterministic build and executable entry;
6. forward process arguments to the framework where arguments are the native
   interface.

Recognizing a flag is not enough: the service must parse and consume it. For
example, Go/go-zero must call `flag.Parse()` before reading the value declared
by `flag.String("f", ...)`, then load application configuration from that path.

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

## Kafka consumption and CLI boundaries

Ordinary Conven CLI starts do not isolate Kafka consumers and may join remote shared groups.
The CLI does not interpret `SERVICE_KAFKA_CONSUMERS_ENABLED`, require source guards,
or inject consumer environment/property switches. Inherited environment and explicit
application settings keep their normal meaning; Conven does not force consumers on.
Legacy `isolation.consumers` entries remain readable metadata but do not enforce
consumer isolation during CLI startup. The plan reports known consumers as unrestricted.
Consumer discovery is informational, not exhaustive broker membership verification.
Guard network protection is a separate, verified boundary. Conven does not alter
business source, provision isolated topics/groups, or modify cluster ACLs.

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

Registry reads retry transient timeouts, EOF/connection failures and HTTP
429/502/503/504 up to four attempts, with 250ms, 500ms and 1s backoff. Each
request retains its 5s timeout. After recovery, the full observation period
starts again against the original pre-start snapshot; failed reads never count
as isolation evidence. Observation has a total budget of the configured period
plus 30s. Authentication errors, invalid responses and new instances still fail
closed; persistent unavailability rolls back startup without deleting registry data.

`--skip-verify` bypasses health, listener, and registry proof together and is
recorded as `unverified(skip-verify)`.

## Runner-only boundary

A manual `generic-runner` may use arbitrary argv and environment. It must not
declare typed `kinds`, is displayed as runner-only, and receives no typed
listener or registration guarantee. Known frameworks with incomplete evidence
cannot silently use this fallback.
