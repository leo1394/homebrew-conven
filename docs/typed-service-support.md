# Typed service support

Conven grants typed trust only when an Analyzer proves the repository facts, a
Certifier selects exactly one compatible policy, and a runtime adapter can
enforce every declared listener and registration rule.

| Runtime/framework | Listener evidence | Deterministic input | Config/registration |
|---|---|---|---|
| go-zero | `RestConf` / `RpcServerConf` | `go.mod` + `go.sum` | Apollo/repository YAML; Consul |
| Go stdlib, Gin, Echo, Fiber, Chi, grpc-go | supported server construction plus `HOST` and `PORT` consumption | `go.mod` + `go.sum` | environment; passive/Kubernetes DNS/Consul/Nacos/Etcd |
| Kratos, Hertz, Kitex | framework server construction plus `HOST` and listener port consumption | `go.mod` + `go.sum` | environment; passive/Consul/Nacos/Etcd |
| Spring Boot | one executable application plus Web and/or gRPC evidence | root `gradlew` or `mvnw`; literal artifact | YAML/properties; passive/Kubernetes DNS/Consul/Nacos/Eureka |
| Quarkus, Micronaut | HTTP controller/path and/or gRPC evidence | root wrapper; literal artifact | native environment keys |
| FastAPI/Starlette | one application object | exactly one supported Python lock input | Uvicorn protected host/port |
| Flask/Django | one WSGI application | exactly one supported Python lock input | Gunicorn protected bind |
| NestJS | HTTP bootstrap and/or `Transport.GRPC` | exactly one Node lockfile ecosystem | protected environment contract |
| Express/Fastify | framework listener bootstrap | exactly one Node lockfile ecosystem | protected `HOST`/`PORT` |
| Bun.serve/Elysia/Hono | Bun listener bootstrap | Bun lockfile only | protected `HOST`/`PORT` |

## Fail-closed cases

`services --registry` aborts atomically for a recognized framework when any of
these facts cannot be proven:

- unique executable entry point and listener kind;
- one deterministic lockfile ecosystem;
- statically determinable artifact where a build artifact is required;
- runtime host and port consumption for environment-based contracts;
- neutral, default-enabled guard around custom registration and deregistration;
- neutral, default-enabled guard around detected Kafka consumer construction;
- exactly one compatible policy and every kind's server route.

A completely unknown repository is reported under skipped repositories with a
reason and does not block other discoveries. A manually declared runner-only
service remains runnable but is never presented as typed.

See [the runtime configuration contract](service-runtime-config-contract.md)
for native arguments, environment keys, and registration guard examples.
