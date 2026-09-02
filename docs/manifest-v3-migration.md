# Manifest v3 migration

Manifest v3 makes typed trust explicit: `kinds`, per-listener health checks,
named registries, registry identity, provider aliases, consumer bindings, and a
certified runtime are no longer inferred at startup.

## Migrate

```bash
conven services --stop-all
conven workspace --migrate
conven workspace --validate
conven doctor --dev
conven services --start --dev --dry-run <service...>
```

Migration refuses to run while a saved service PID is active. It writes a
pre-migration backup under `.conven/backups`, validates the complete candidate,
and atomically replaces `.conven/conven.yaml`. Repeating migration on v3 does
not rewrite the file.

Until migration completes, normal workspace commands refuse v1/v2 manifests.
`version`, help, `workspace --migrate`, and `services --stop-all` remain
available so the workspace can be recovered safely.

## Field mapping

| Old field | v3 result |
|---|---|
| `kind` | `kinds: [kind]` |
| `health` | one `healthChecks` entry bound to the listener |
| `discovery.bindings` | `discovery.consumerBindings` |
| dependency binding aliases | provider `discovery.providerAliases` when ownership is provable |
| `environment.registry` | a named `environment.registries` declaration when its address is provable |
| v2 explicit runner/env service | `generic-runner` runner-only service |

Migration never treats consumer bindings as provider aliases. If registry
identity, runtime, registry address, or alias ownership cannot be proven, the
whole migration fails and reports the exact field to add before retrying.

## Review after migration

- Confirm each typed kind has a port, policy server route, and health check.
- Confirm `identity` is the exact name used in Consul, Nacos, Eureka, or Etcd.
- Confirm `providerAliases` describe how consumers refer to the provider, while
  `consumerBindings` describe clients owned by the current service.
- Keep credentials in the environment through `tokenEnv`, `usernameEnv`, and
  `passwordEnv`; never place secret values in the manifest.
- Run `services --registry` only after matching policies exist. Its update is
  atomic, so a recognized but uncertifiable repository changes nothing.
