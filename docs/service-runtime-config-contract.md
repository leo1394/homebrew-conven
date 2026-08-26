# Service runtime configuration contract

This document defines how an application consumes configuration materialized by
Conven. It applies to service authors, workspace manifest maintainers, and
trusted-adapter implementers.

For Chinese, see [服务运行时配置契约](service-runtime-config-contract-zh.md).

## 1. Required behavior

Conven materializes repository or remote configuration into a service-scoped
runtime directory:

```text
.conven/runtime/current/configs/<service>/
```

The executable artifact and runtime configuration remain separate:

```text
source ──build──> runtime/current/artifacts/<service>
config ──copy + patch──> runtime/current/configs/<service>
```

A typed service must:

1. accept the configuration argument declared by its trusted adapter;
2. parse that argument;
3. load configuration from the referenced runtime directory or file;
4. exit non-zero when the argument is missing;
5. exit non-zero when the path is invalid or configuration cannot be parsed;
6. never silently fall back to repository, classpath, or build-time defaults;
7. derive listener, port, registration, and dependency routes from the final
   runtime configuration;
8. remain in the foreground and expose a Conven-verifiable health check.

Receiving argv alone does not satisfy the contract:

```text
OS supplies argv
  → language runtime exposes argv
  → service parses the adapter option
  → service loads configuration from that path
```

The implementation is incompatible if the final step is missing.

## 2. Tool-agnostic application source

Application source should not depend on `CONVEN_CONFIG_DIR` or another
Conven-specific name. It should consume a framework-native or project-native
option:

| Language/framework | Adapter option |
| --- | --- |
| Go/go-zero | `-f <runtime-config-directory>` |
| Java/Spring Boot | `--spring.config.location=file:<runtime-config-directory>/` |
| Python | `--config <runtime-config-file>` |
| Node.js | `--config <runtime-config-file>` |
| Dart | `--config <runtime-config-file>` |
| Rust | `--config <runtime-config-file>` |

The manifest or policy adapts Conven's `${configDir}` template to native argv:

```yaml
runner:
  run: [python, -m, catalog_api, --config, "${configDir}/application.yaml"]
```

`${configDir}` expands to an absolute path before execution. The application
only knows about `--config`.

Conven may still inject `CONVEN_CONFIG_DIR` into prepare, build, health, and run
processes as orchestration metadata. It is not the recommended application-source
integration API.

## 3. Manifest and policy responsibilities

### 3.1 Runner-only services

A runner-only service has no typed isolation contract and may use any
project-native option:

```yaml
services:
  report-worker:
    path: services/report-worker
    runner:
      run: [python, -m, report_worker, --config, "${configDir}/application.yaml"]
    health:
      type: process
```

Conven expands and passes the argument without assuming its semantics.

### 3.2 Typed services

A typed service's trusted adapter must define:

- how runtime configuration is materialized;
- which option passes the configuration path;
- whether that path denotes a directory or a file;
- how listener and registration isolation are verified;
- how actual configuration consumption is proven.

The current built-in trusted adapter covers go-zero, Consul, and `yaml-overlay`
only. It requires:

```yaml
policies:
  go-zero-consul:
    process:
      args: [-f, "${configDir}"]
```

The final command may contain only the executable and one verified
`-f <absolute-config-directory>` argument.

Other languages can run as runner-only services. Equivalent automatic routing
and isolation require a trusted adapter for that framework, not just a different
language label.

## 4. argv placement

The option must appear in application argv, not interpreter or VM argv:

```text
Go executable     service -f <dir>
Java JAR          java -jar service.jar --spring.config.location=file:<dir>/
Python module     python -m service --config <file>
Node.js script    node dist/server.js --config <file>
Dart source       dart run bin/server.dart --config <file>
Dart executable   service --config <file>
Rust executable   service --config <file>
```

This Python command is invalid because `--config` is passed to the interpreter:

```text
python --config <file> -m service
```

Manifest commands are argv arrays and do not pass through a shell. Use:

```yaml
run: ["${artifact}", --config, "${configDir}/application.yaml"]
```

Do not rely on shell expansion:

```yaml
run: ["${artifact}", --config, "$CONVEN_CONFIG_DIR/application.yaml"]
```

## 5. Go / go-zero

The current trusted go-zero adapter defines `-f` as the runtime configuration
directory, not a single YAML file. The service bootstrap must preserve that
meaning.

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

Manifest:

```yaml
runner:
  build: [go, build, -o, "${artifact}", ./cmd/server]
  run: ["${artifact}"]

policies:
  go-zero-consul:
    process:
      args: [-f, "${configDir}"]
```

An invalid implementation parses the option but ignores it:

```go
func main() {
	var configDir string
	flag.StringVar(&configDir, "f", "", "runtime config directory")
	flag.Parse()

	// Invalid: repository configuration is still loaded.
	configuration, _ := loadConfig("etc/config-local.yaml")
	run(configuration)
	_ = configDir
}
```

Skipping `flag.Parse()`, treating the directory as a YAML file, ignoring the
parsed value, or falling back to `etc/` all violate the contract.

## 6. Java / Spring Boot

Spring Boot should use its native `spring.config.location`; application source
does not need to recognize Conven. The option appears after the JAR:

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

The entry point forwards argv to Spring Boot:

```java
public final class CatalogApplication {
    public static void main(String[] args) {
        SpringApplication.run(CatalogApplication.class, args);
    }
}
```

The runtime directory must contain a Spring-recognized `application.yaml`.
`spring.config.location` replaces the default search locations. Do not use an
`optional:` location or application fallback to classpath configuration. See
[Spring Boot Externalized Configuration](https://docs.spring.io/spring-boot/reference/features/external-config.html)
for the location and fail-closed behavior.

A Spring trusted adapter must also verify equivalent runtime properties:

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

Manifest:

```yaml
runner:
  run: [python, -m, catalog_api, --config, "${configDir}/application.yaml"]
```

Do not use `parse_known_args()` to ignore an unknown configuration option, and
do not give `--config` a repository-default path.

## 8. Node.js

This example has no argument-parser dependency:

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

Manifest:

```yaml
runner:
  build: [npm, run, build]
  run: [node, dist/server.js, --config, "${configDir}/application.yaml"]
```

With commander, yargs, or another parser, keep the option required and terminate
the process when configuration loading fails.

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

Manifest:

```yaml
runner:
  build: [dart, compile, exe, bin/server.dart, -o, "${artifact}"]
  run: ["${artifact}", --config, "${configDir}/application.yaml"]
```

When using `package:args`, make `--config` required and preserve the same
fail-closed behavior.

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

Manifest:

```yaml
runner:
  build: [cargo, build]
  artifact: "${serviceDir}/target/debug/catalog-api"
  run: ["${artifact}", --config, "${configDir}/application.yaml"]
```

The project may use clap instead, as long as the option is required and no
repository fallback exists.

## 11. Adapter contract test

Static validation can prove that an argument is passed, but not that the service
uses it. Every trusted adapter needs a behavioral test.

Recommended canary test:

1. make repository-default configuration listen on port `18080`;
2. create temporary runtime configuration on a random free port such as `29137`;
3. disable remote service registration in the runtime configuration;
4. start the service with the adapter option;
5. wait for health on `127.0.0.1:29137`;
6. verify that the test process is not listening on `127.0.0.1:18080`;
7. verify that the registry contains no local instance;
8. stop the process;
9. remove the runtime configuration and verify that a second start exits
   non-zero.

This detects:

- no argument parsing;
- a parsed but unused value;
- disagreement over directory versus file semantics;
- fallback after configuration failure;
- listener or registration behavior not controlled by runtime configuration.

Do not accept the option's appearance in `--help` as proof. Help output proves a
parser declaration, not configuration consumption.

## 12. Conven verification boundary

Conven can verify:

- the manifest or policy produces adapter-compliant argv;
- the option references the guarded absolute runtime path;
- the materialized listener and registration guards are correct;
- the process passes health on the declared local address;
- the trusted adapter contract test covers canary behavior.

Conven cannot reliably prove arbitrary application behavior with language-neutral
static analysis. The framework adapter's behavioral test provides that proof;
source-string searches do not.

## 13. Integration checklist

Before accepting a typed service or trusted adapter, verify:

- [ ] application source contains no Conven-specific integration name;
- [ ] the manifest or policy maps `${configDir}` to native argv;
- [ ] the option is in application argv, not interpreter or VM argv;
- [ ] a missing option exits non-zero;
- [ ] an invalid path or configuration exits non-zero;
- [ ] there is no repository or classpath fallback;
- [ ] listener and port come from runtime configuration;
- [ ] local RPC/service registration is disabled by runtime configuration;
- [ ] health checks use the manifest-declared local address;
- [ ] the canary-port test detects an ignored argument;
- [ ] the service remains in the foreground and Conven can stop it.
