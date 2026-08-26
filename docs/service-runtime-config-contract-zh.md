# 服务运行时配置契约

本文定义业务服务如何消费 Conven 生成的运行时配置。它适用于服务开发者、workspace
Manifest 维护者和 trusted adapter 实现者。

英文版见 [Service runtime configuration contract](service-runtime-config-contract.md)。

## 1. 核心要求

Conven 将仓库配置或远程配置物化到服务独立的运行时目录：

```text
.conven/runtime/current/configs/<service>/
```

编译产物与配置副本相互独立：

```text
source ──build──> runtime/current/artifacts/<service>
config ──copy + patch──> runtime/current/configs/<service>
```

typed service 必须满足以下契约：

1. 接受 trusted adapter 声明的配置参数；
2. 解析该参数；
3. 使用参数指向的运行时目录或文件加载配置；
4. 参数缺失时以非零状态退出；
5. 路径不存在、不是预期类型或配置解析失败时以非零状态退出；
6. 不得静默回退到仓库配置、classpath 配置或编译期默认配置；
7. listener、端口、注册和依赖路由必须来自最终运行时配置；
8. 服务必须保持前台运行，并提供 Conven 可验证的健康检查。

仅在 argv 中收到参数不等于履行契约：

```text
OS 传入 argv
  → 语言运行时暴露 argv
  → 服务解析 Adapter 参数
  → 服务实际从该路径加载配置
```

最后一步缺失时仍是不兼容实现。

## 2. 工具无关的业务源码

业务源码不应依赖 `CONVEN_CONFIG_DIR` 或其他 Conven 专用名称。它应使用框架原生或
项目原生的配置参数，例如：

| 语言/框架 | Adapter 参数 |
| --- | --- |
| Go/go-zero | `-f <runtime-config-directory>` |
| Java/Spring Boot | `--spring.config.location=file:<runtime-config-directory>/` |
| Python | `--config <runtime-config-file>` |
| Node.js | `--config <runtime-config-file>` |
| Dart | `--config <runtime-config-file>` |
| Rust | `--config <runtime-config-file>` |

Manifest 或 Policy 负责将 Conven 的 `${configDir}` 模板转换为服务原生 argv：

```yaml
runner:
  run: [python, -m, catalog_api, --config, "${configDir}/application.yaml"]
```

`${configDir}` 在执行前展开为绝对路径。业务源码只认识 `--config`。

Conven 可以继续向 `prepare`、`build`、health command 和运行过程注入
`CONVEN_CONFIG_DIR` 作为编排元数据，但它不是业务服务的推荐接入 API。

## 3. Manifest 和 Policy 的职责

### 3.1 Runner-only service

runner-only service 不声明 typed isolation contract，可以使用任意项目原生参数：

```yaml
services:
  report-worker:
    path: services/report-worker
    runner:
      run: [python, -m, report_worker, --config, "${configDir}/application.yaml"]
    health:
      type: process
```

Conven 负责展开和传递参数，但不会假定参数语义。

### 3.2 Typed service

typed service 的 trusted adapter 必须同时定义：

- 如何物化运行时配置；
- 使用哪个参数传入配置路径；
- 参数表示目录还是文件；
- 如何验证 listener 和 registration 隔离；
- 如何证明服务实际消费了配置。

当前内置的 trusted adapter 仅支持 go-zero、Consul 和 `yaml-overlay`，并要求：

```yaml
policies:
  go-zero-consul:
    process:
      args: [-f, "${configDir}"]
```

最终命令只能包含 executable 和一个经过验证的 `-f <absolute-config-directory>`。

其他语言可以作为 runner-only service 运行。要获得同等级别的自动配置改写和隔离验证，
需要为对应框架实现 trusted adapter，不能只修改语言名称。

## 4. argv 位置

参数必须出现在业务应用参数位置，而不是解释器或虚拟机参数位置：

```text
Go executable     service -f <dir>
Java JAR          java -jar service.jar --spring.config.location=file:<dir>/
Python module     python -m service --config <file>
Node.js script    node dist/server.js --config <file>
Dart source       dart run bin/server.dart --config <file>
Dart executable   service --config <file>
Rust executable   service --config <file>
```

例如下面的 Python 命令是错误的，因为 `--config` 被传给解释器而不是应用：

```text
python --config <file> -m service
```

Manifest command 是 argv 数组，不经过 shell。应使用：

```yaml
run: ["${artifact}", --config, "${configDir}/application.yaml"]
```

不要依赖 shell 展开：

```yaml
run: ["${artifact}", --config, "$CONVEN_CONFIG_DIR/application.yaml"]
```

## 5. Go / go-zero

当前 trusted go-zero adapter 的 `-f` 值是运行时配置目录，而不是单个 YAML 文件。服务
bootstrap 必须遵循这个语义。

```go
package main

import (
	"flag"
	"log"
	"path/filepath"
)

func main() {
	var configDir string
	flag.StringVar(&configDir, "f", "", "runtime config directory")
	flag.Parse()
	if configDir == "" {
		log.Fatal("-f runtime config directory is required")
	}

	configPath := filepath.Join(configDir, "config-local.yaml")
	configuration, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("load runtime config %s: %v", configPath, err)
	}
	run(configuration)
}
```

Manifest：

```yaml
runner:
  build: [go, build, -o, "${artifact}", ./cmd/server]
  run: ["${artifact}"]

policies:
  go-zero-consul:
    process:
      args: [-f, "${configDir}"]
```

不兼容实现：

```go
func main() {
	var configDir string
	flag.StringVar(&configDir, "f", "", "runtime config directory")
	flag.Parse()

	// 错误：虽然解析了 -f，仍然读取仓库配置。
	configuration, _ := loadConfig("etc/config-local.yaml")
	run(configuration)
	_ = configDir
}
```

没有调用 `flag.Parse()`、把目录当作 YAML 文件、解析后不使用或在失败后回退到 `etc/`
都不符合契约。

## 6. Java / Spring Boot

Spring Boot 应使用框架原生的 `spring.config.location`，而不是在业务代码中识别 Conven。
参数位于 JAR 之后：

```yaml
runner:
  build: [./gradlew, bootJar]
  artifact: "${serviceDir}/build/libs/catalog-api.jar"
  run:
    - java
    - -jar
    - "${artifact}"
    - "--spring.config.location=file:${configDir}/"
```

入口函数将 argv 原样交给 Spring Boot：

```java
public final class CatalogApplication {
    public static void main(String[] args) {
        SpringApplication.run(CatalogApplication.class, args);
    }
}
```

运行时目录至少包含 Spring Boot 可以识别的 `application.yaml`。使用
`spring.config.location` 替换默认搜索位置；不要使用带 `optional:` 的路径，也不要在
加载失败后自行回退到 classpath 配置。具体加载规则见
[Spring Boot Externalized Configuration](https://docs.spring.io/spring-boot/reference/features/external-config.html)。

Spring trusted adapter 还必须验证运行时配置中的等价属性，例如：

```yaml
server:
  address: 127.0.0.1
  port: 18080

spring:
  cloud:
    consul:
      discovery:
        register: false
```

## 7. Python

```python
import argparse
from pathlib import Path


def parse_arguments():
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    return parser.parse_args()


def main():
    arguments = parse_arguments()
    config_path = Path(arguments.config)
    if not config_path.is_file():
        raise SystemExit(f"runtime config is not a file: {config_path}")
    configuration = load_config(config_path)
    run(configuration)


if __name__ == "__main__":
    main()
```

Manifest：

```yaml
runner:
  run: [python, -m, catalog_api, --config, "${configDir}/application.yaml"]
```

不要使用 `parse_known_args()` 后忽略未知配置参数，也不要为 `--config` 提供指向仓库
配置的默认值。

## 8. Node.js

以下示例不依赖第三方参数库：

```javascript
import fs from "node:fs";

function optionValue(name) {
  const index = process.argv.indexOf(name);
  if (index < 0 || index + 1 >= process.argv.length) {
    throw new Error(`${name} is required`);
  }
  return process.argv[index + 1];
}

const configPath = optionValue("--config");
if (!fs.statSync(configPath).isFile()) {
  throw new Error(`runtime config is not a file: ${configPath}`);
}

const configuration = loadConfig(configPath);
await run(configuration);
```

Manifest：

```yaml
runner:
  build: [npm, run, build]
  run: [node, dist/server.js, --config, "${configDir}/application.yaml"]
```

使用 commander、yargs 或其他解析器时，也必须将参数声明为 required，并让配置加载失败
终止进程。

## 9. Dart

```dart
import 'dart:io';

String requiredOption(List<String> args, String name) {
  final index = args.indexOf(name);
  if (index < 0 || index + 1 >= args.length) {
    throw ArgumentError('$name is required');
  }
  return args[index + 1];
}

void main(List<String> args) {
  final configPath = requiredOption(args, '--config');
  final file = File(configPath);
  if (!file.existsSync()) {
    stderr.writeln('runtime config is not a file: $configPath');
    exit(2);
  }
  final configuration = loadConfig(file.readAsStringSync());
  run(configuration);
}
```

Manifest：

```yaml
runner:
  build: [dart, compile, exe, bin/server.dart, -o, "${artifact}"]
  run: ["${artifact}", --config, "${configDir}/application.yaml"]
```

如果使用 `package:args`，将 `--config` 声明为必填并保持相同的 fail-closed 行为。

## 10. Rust

```rust
use std::{env, fs, path::PathBuf, process};

fn required_option(name: &str) -> String {
    let arguments: Vec<String> = env::args().collect();
    arguments
        .windows(2)
        .find(|pair| pair[0] == name)
        .map(|pair| pair[1].clone())
        .unwrap_or_else(|| {
            eprintln!("{} is required", name);
            process::exit(2);
        })
}

fn main() {
    let config_path = PathBuf::from(required_option("--config"));
    let source = fs::read_to_string(&config_path).unwrap_or_else(|error| {
        eprintln!("load runtime config {}: {}", config_path.display(), error);
        process::exit(2);
    });
    let configuration = load_config(&source).unwrap_or_else(|error| {
        eprintln!("parse runtime config: {}", error);
        process::exit(2);
    });
    run(configuration);
}
```

Manifest：

```yaml
runner:
  build: [cargo, build]
  artifact: "${serviceDir}/target/debug/catalog-api"
  run: ["${artifact}", --config, "${configDir}/application.yaml"]
```

项目也可以使用 clap，只要配置参数为 required 且没有源码配置 fallback。

## 11. Adapter contract test

静态检查只能证明参数被正确传入，不能证明服务真正使用了它。trusted adapter 必须包含
行为验证。

推荐 canary 测试：

1. 仓库默认配置监听端口 `18080`；
2. 创建临时 runtime config，监听随机空闲端口，例如 `29137`；
3. 在 runtime config 中关闭远程服务注册；
4. 使用 Adapter 参数启动服务；
5. 等待 `127.0.0.1:29137` 的健康检查成功；
6. 验证 `127.0.0.1:18080` 没有由测试进程监听；
7. 验证注册中心不存在该本地实例；
8. 停止进程；
9. 删除 runtime config 后重新启动，验证进程以非零状态退出。

这个测试可以发现：

- 参数完全没有解析；
- 参数解析后没有使用；
- 参数的目录/文件语义不一致；
- 配置失败后回退到仓库默认值；
- listener 或 registration 没有由运行时配置控制。

不要仅通过 `--help` 中出现参数来判定兼容。help 只能证明 parser 声明，不能证明实际
配置消费。

## 12. Conven 验证边界

Conven 可以验证：

- Manifest/Policy 生成了 Adapter 规定的 argv；
- 参数指向受保护的绝对 runtime config 路径；
- 物化配置中的 listener 和 registration guards 正确；
- 进程在声明的本地端口通过健康检查；
- trusted adapter contract test 已覆盖 canary 行为。

Conven 无法通过通用静态分析可靠证明任意语言的业务代码使用了参数。该证明应由
framework adapter 的行为测试完成，而不是依赖源码字符串搜索。

## 13. 接入检查清单

提交一个 typed service 或 trusted adapter 前确认：

- [ ] 业务源码不引用 Conven 专用名称；
- [ ] Manifest/Policy 使用 `${configDir}` 生成框架原生 argv；
- [ ] 参数位置位于业务应用 argv，而不是解释器/VM argv；
- [ ] 参数缺失时非零退出；
- [ ] 路径或配置无效时非零退出；
- [ ] 没有仓库配置或 classpath 配置 fallback；
- [ ] listener 和端口来自 runtime config；
- [ ] 本地 RPC/服务注册通过 runtime config 禁用；
- [ ] health check 使用 Manifest 声明的本地地址；
- [ ] canary 端口测试能够发现参数被忽略；
- [ ] 服务保持前台运行并能被 Conven 正常停止。
