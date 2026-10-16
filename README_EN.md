# LaxCode Cloud

[中文](./README.md)

LaxCode is a Go and React coding agent with a browser UI. This `cloud` branch
contains the cloud deployment; local mode is maintained on a separate branch.

## Quick start

Install Go >= 1.26.6, pnpm, Docker and rg. Run as a non-root service account with Docker access:

```sh
make build-cloud-bash-image
./cloud.sh
```

Open `http://127.0.0.1:5173`, create a project and configure an API key and Base URL.
The backend always runs in cloud mode; no mode flag is required.
Use `LAXCODE_CLOUD_PORT=5180 ./cloud.sh` to choose another web port.

## Runtime

- Each guest UUID has separate projects, model settings, sessions and Skills under
  `$HOME/laxcode/{user_id}/`. Guest UUIDs are prototype identity; deployments need authentication at the entry point.
- Models use the upstream Responses API directly. API keys live in the service account's
  `$HOME/.laxcode/configs.db`, outside agent workspaces.
- Bash runs in a fresh offline Docker container. Projects and user tmp are writable;
  Skills are mounted read-only. Missing Docker never falls back to a host shell.
- File tools use rooted directory handles to enforce the user boundary.
- Chat/Resume tracks project changes, including Bash outputs, for preview and ZIP download.
- Context compaction, session evaluation, performance monitoring and OpenTelemetry are supported.
- The service accepts at most two concurrent Chat/Resume requests across all users.

Default model and Skill templates come from `$HOME/.laxcode`, or the directory selected by
`-conf_copy_dir`. Existing users keep their own settings. Back up both `configs.db` and user data.

## Deployment and development

`make build-cloud` creates a Linux amd64 NAS bundle with embedded frontend assets,
startup scripts and an offline Bash image archive. See the [deployment guide](./deploy/cloud/README.md).

```sh
go test ./...
go vet ./...
pnpm --dir web test
pnpm --dir web lint
pnpm --dir web build
```

The dependency direction is `cmd → application → domain ← infrastructure`.
The cloud dispatcher owns user repositories and model managers; agents receive request-scoped clients.
See [settings](./docs/settings.md), [session storage](./docs/session-storage-engine.md)
and [context compaction](./docs/context-compaction-design.md) for details.
