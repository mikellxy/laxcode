# LaxCode Cloud

Single-instance deployment: the host runs Docker Engine, OpenShell 0.1.4 and laxcode-proxy. The proxy serves the UI and routes each X-USERID UUID to its own sandbox. laxcode-cloud and its shell tools run inside that sandbox.

Configure deploy/defaults/settings.json, run `make build-cloud`, copy `deploy/deploy.sh` and `deploy/laxcode.tar.gz` to `~/laxcode_deploy`, then run `~/laxcode_deploy/deploy.sh restart`. Open http://localhost:5173. User files live under ~/laxcode; sandbox state is temporary across deployment replacement. Provider secrets stay with OpenShell and users select from the managed model catalog.

See [deployment instructions](deploy/README.md) and [model configuration](docs/settings.md). This beta uses guest identifiers without authentication or billing. The proxy listens on port 5173; the dedicated gateway binds only to loopback. Preinstall the fixed toolchain/runtime images before deployment.
