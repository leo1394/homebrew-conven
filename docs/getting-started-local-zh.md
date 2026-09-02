# Conven 纯本地开发新手手册

本文适用于项目刚开始开发、没有 Kubernetes 集群，也没有远程数据库或 Kafka 的场景。
Conven 负责本地业务 Service 的选择、启动顺序、运行时配置、健康检查和日志；PostgreSQL、
Redis、Kafka、MinIO 等基础设施由开发者使用项目已有方式启动，Conven 只连接其地址。

## 1. 初始化 workspace

在包含各业务 Service 的目录中执行：

```bash
conven init --local
conven services --list
conven status
```

`init --local` 创建 Manifest v3 和 `connection.driver: none` 的 `local` 环境，不生成
Compose 文件，也不要求 Docker。请先确认 `user-svc`、`order-svc` 等服务已被识别；
必要时使用 `conven workspace --edit` 完善 runner、端口和依赖关系。

## 2. 启动项目基础设施

使用团队已有方式启动数据库和消息中间件，例如本机软件包、项目脚本或项目自己的
Docker Compose。Conven 不安装、不启动、不停止，也不删除这些资源。

确认服务监听地址，例如：

- PostgreSQL：`127.0.0.1:5432`
- Redis：`127.0.0.1:6379`
- Kafka：`127.0.0.1:9092`

## 3. 声明 Endpoint 和 Service 依赖

在 `.conven/conven.yaml` 的 `local` 环境中声明可访问地址，并由 Service 引用：

```yaml
version: 3

workspace:
  name: shop

environments:
  local:
    connection:
      driver: none
    endpoints:
      postgres:
        protocol: tcp
        address: 127.0.0.1:5432
      redis:
        protocol: tcp
        address: 127.0.0.1:6379

services:
  user-svc:
    path: services/user-svc
    runner:
      run: [go, run, ./cmd/user-svc]
    ports:
      rpc: 18081

  order-svc:
    path: services/order-svc
    runner:
      run: [go, run, ./cmd/order-svc]
    ports:
      http: 18080
    dependencies:
      user-svc:
        localService: user-svc
        env:
          USER_RPC_ADDRESS: ${dependency.address}
      postgres:
        env:
          DATABASE_URL: postgres://dev:dev@${dependency.address}/shop
      redis:
        env:
          REDIS_ADDRESS: ${dependency.address}
```

Endpoint 名称与 Service 依赖别名一致时，`local` 环境会自动解析到该 Endpoint。
Conven 会展开 `${dependency.address}`，并在启动前执行 readiness 检查。连接凭据可放入
`environments.local.envFile` 指向的私有文件，不要提交密钥。

## 4. 校验并启动业务 Service

```bash
conven doctor --env local
conven services --start --env local --dry-run user-svc order-svc
conven services --start --env local user-svc order-svc
```

Service 参数是显式启动范围。只启动 `order-svc` 时，默认不会自动启动 `user-svc`：

```bash
conven services --start order-svc
```

确实需要递归加入本地业务 Service 依赖时，使用名称明确的选项：

```bash
conven services --start order-svc --with-dependencies
```

该选项只扩展业务 Service，不管理 PostgreSQL、Redis 或 Kafka。

## 5. 查看与停止

```bash
conven status
conven services --status
conven services --dashboard
conven services --logs --tail order-svc
conven services --stop-all
```

`conven status` 会列出 workspace、Service、配置的 Endpoint、disabled bindings 和当前
运行状态。Dashboard 展示本地 Service、disabled bindings、启动时间及实时日志。
`services --stop-all` 只停止 Conven 启动的 Service 和连接，不影响开发者启动的数据库
或消息中间件。

## 常见问题

- Endpoint readiness 失败：先确认基础设施已启动，再检查地址、端口和防火墙。
- Service 能收到参数但配置未生效：入口必须解析 runner 声明的配置参数，并从 Conven
  生成的运行时路径加载配置，不能回退到仓库默认文件。
- 不知道为什么连接集群：检查所选环境的 `connection.driver`；`none` 不连接集群，
  `ktctl` 才会建立本机到集群的连接。
- 需要重建数据库或清理 volume：使用项目自己的基础设施脚本处理，不属于 Conven
  生命周期。

完整的多语言入口约定见[服务运行时配置契约](service-runtime-config-contract-zh.md)。
