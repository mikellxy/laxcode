# LaxCode Cloud

Single-instance deployment: the host runs Docker Engine, OpenShell 0.1.4 and laxcode-proxy. The proxy serves the UI and routes each X-USERID UUID to its own sandbox. laxcode-cloud and its shell tools run inside that sandbox.

Configure `deploy/defaults/settings.json`, then run `cd deploy && sh build.sh` on your workstation or CI. The only artifact is `release/laxcode.tar.gz` at the repository root, always targeting Linux amd64 and ignored by Git. Upload it to private TOS storage or copy it to `/root/laxcode_deploy/laxcode.tar.gz` on ECS.

On ECS, run `cd /root/laxcode_deploy && tar --unlink-first -xzf laxcode.tar.gz && cd laxcode && python3 deploy.py`. The packaged script restarts manager/proxy directly, without installing systemd services, staging release directories or retaining old versions. It replaces `laxcode-cloud:current` and removes old application images. Runtime state, user files and the quota ledger are preserved. After an ECS reboot, run the deployment command manually.

See [deployment instructions](deploy/README.md) and [model configuration](docs/settings.md). This beta uses guest identifiers without authentication or billing. The proxy listens on port 5173; the dedicated gateway binds only to loopback. Preinstall the fixed toolchain/runtime images before deployment.
