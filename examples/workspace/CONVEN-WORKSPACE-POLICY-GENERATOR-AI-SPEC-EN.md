---
spec: conven-workspace-policy-generator
version: 3
language: en
repository: "https://github.com/leo1394/homebrew-conven"
pluginInvocation: "conven plugins --run [NAME] [plugin-args...]"
profile: go-zero-apollo-consul-v1
---

# Conven Workspace Policy Generator: AI Implementation Specification

The generator maintains a complete Manifest v3 candidate.
`.conven/conven.yaml` is the only service inventory. Do not read or recreate
`.conven/catalog.yaml`, property-based service lists, or hard-coded service
presets.

## Inputs

1. Current `.conven/conven.yaml`: preserve registry-proven paths, `kinds`,
   ports, runners, health checks, Analyzer/Certifier evidence, identity, and
   consumer-isolation evidence plus manual fields under `services`.
2. `.conven/conven-generator.json`: only ambiguous `bindingProviders`
   mappings; never environments, policies, service paths, kinds, or ports.
3. Direct child repositories: read-only static scanning. Never build, run a
   package manager, access the network, or edit business source.

Recommended input:

```json
{
  "version": 1,
  "bindingProviders": {
    "partnerRpc": "partner-service"
  }
}
```

Credentials may only reference environment variables. Never write credential
values to JSON, the manifest, logs, or errors.

## Output rules

- Emit one deterministic UTF-8 strict-schema `version: 3` YAML document.
- `workspace.disabledBindings` is the only disabled-binding set.
- `discovery.consumerBindings` belongs to the current consumer.
- `discovery.providerAliases` names this provider from other consumers.
- `discovery.consumers` and `isolation.consumers` are registry-certified
  runtime safety facts; preserve them together and never synthesize one alone.
- Resolve ambiguous binding ownership only through `bindingProviders`; never
  guess by case normalization.
- Every typed kind has a port, Policy server route, and health check.
- Multi-listener services use `kinds`; service-level `network.listen` applies
  to every listener in the process.
- Never delete an unknown but valid registry service, runner, port, or
  certification.
- Validate references, unique ports, dependencies, registries, and credential
  references before writing; a failure changes no target.

## CLI

The plugin must support:

```text
conven plugins --run [NAME] [plugin-args...]
  --workspace PATH
  --stdout
  --check
  --output [FILE]
  --disable-bindings BINDING...
```

`--stdout` writes no file. `--check` compares the default output. `--output`
without FILE writes `application.yaml`. Explicit file output may atomically
replace an existing candidate. Publish through a same-directory temporary file,
fsync, and atomic rename with permissions no wider than `0600`.

Post-generation workflow:

```bash
conven plugins --run --output
conven workspace --import --edit
conven workspace --validate
conven doctor
conven services --start --dry-run <service...>
```

## Acceptance

- Repeated generation is byte-identical.
- Input or certification failure changes neither the active manifest nor an
  existing candidate.
- Every direct child repository is retained/generated or has a concrete skip
  reason.
- No secrets, kubeconfig contents, absolute credential paths, or Conven-specific
  business-source variables.
- After `conven services --registry`, a new service keeps its port, runner, and
  certification through the next generation.
