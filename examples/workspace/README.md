# Conven Workspace Quick Start

This workspace was initialized for Conven. Conven starts only the selected
local services, keeps unselected dependencies on remote discovery, and
aggregates local logs.

Conven repository: [leo1394/homebrew-conven](https://github.com/leo1394/homebrew-conven)

## Initialized files

- `.conven/conven.yaml` is the active Conven workspace manifest.
- `services.properties` is the generator service catalog. Add one service per
  line using one of the record formats documented in that file. Service ports
  must be unique.
- `disabled-services.properties` lists RPC bindings that the generator must
  disable in local runtime configuration. Add one binding per line.
- `CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md` is a complete AI-readable
  contract for implementing or updating a workspace policy generator plugin.

`conven init` never overwrites these files. The first init performs the same
direct-child repository scan used by `conven services --registry`; it does not
need to run that command a second time. Review and edit the generator inputs
before generating a policy.

## Create and install the generator

Give `CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md` to an AI together with this
workspace. Ask it to scan the direct child repositories and create a Python 3
generator plugin that follows the specification. Complete any explicit
generator inputs requested by the specification.

The generic specification requires a workspace-specific
`conven-generator.json`; `init` cannot safely invent its environments or
connection policy. Create and review it before the first generator run. A
minimal no-connection example is:

```json
{
  "version": 1,
  "profile": "go-zero-apollo-consul-v1",
  "policyName": "local-services",
  "environments": {
    "dev": {
      "registry": "consul",
      "connection": {"driver": "none"}
    }
  }
}
```

Use the field contract and `ktctl` example in
`CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md` when the workspace needs a
cluster connection. Do not put credentials or kubeconfig contents in this
JSON file.

Install the generated plugin in this workspace and inspect the available
plugins:

```bash
conven plugins --install ./generate-workspace-policy.py
conven plugins --list
```

## Generate and import the policy

When the workspace has exactly one plugin, its name may be omitted. With no
filename after `--output`, the plugin writes `application.yaml` and asks before
overwriting an existing file:

```bash
conven plugins --run --output
conven policy --import --edit
```

If more than one plugin is installed, provide the plugin name explicitly:

```bash
conven plugins --run generate-workspace-policy --output
```

To replace the disabled binding file for one generation, pass the bindings
after `--disable-bindings`:

```bash
conven plugins --run --output --disable-bindings legacyRpc experimentalRpc
```

## Validate and run services

Choose an environment supported by the imported manifest. Replace `<env>` and
the example placeholders with values shown by `conven services --list`:

```bash
conven services --list
conven doctor --env <env>
conven services --start --env <env> --dry-run <service...>
conven services --start --env <env> <service...>
conven services --status
conven services --logs <service>
conven services --stop-all
```

Run `conven services --registry` again after adding, removing, or renaming a
direct child service repository. It updates discovered facts in
`.conven/conven.yaml` without overwriting manual service configuration unless
pruning is explicitly requested.

The registry command never edits `services.properties` or
`disabled-services.properties`. Conven cannot safely infer unique local ports
or policy-level provider aliases from every repository. Keep the service
catalog as a user- or AI-reviewed superset: add newly discovered repositories
with verified ports, retain entries for repositories that are not currently
checked out, and remove entries only by an explicit review. Even
`services --registry --prune` changes only the manifest.
