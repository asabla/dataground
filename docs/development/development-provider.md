# Configure the development Codex provider

`go run ./cmd/dataground-development-provider` installs the fixed `codex` provider on an existing [operator-owned development gateway](development-gateway.md). This supplies the provider that the governed worker selects. OpenShell retains the credentials and performs provider injection. The command does not put credentials in the execution plan, sandbox, command arguments, receipt, or deployment records. It does not acquire credentials, grant workload access, or establish runtime certification.

Use the same Linux host, user, gateway arguments, and private workspace as the gateway command. Install the pinned OpenShell `0.0.86` CLI and provide its absolute path. The strict candidate gateway remains Linux ARM64 only. Stop worker execution during setup. Exclude concurrent direct OpenShell or Docker administration: the shared per-run lock serializes these DataGround operator commands, but it cannot lock an independent native administrator.

Prepare a fresh owner-only directory outside both the repository and gateway workspace. Its parent and directory must have mode `0700`. The directory must contain exactly four owner-only, single-link regular files with mode `0600`: `access_token`, `refresh_token`, `account_id`, and `id_token`. Each file holds the corresponding credential bytes without a trailing newline. The existing credential-source boundary permits at most 64 KiB per file, checks ownership and file identity, and consumes the exact bundle before returning its contents. Use a temporary copy from the approved credential holder, never the holder's persistent source. Do not place values in shell arguments or diagnostic output.

Use the exact gateway pins, a stable operator identity, and a fresh correlation identifier:

```sh
go run ./cmd/dataground-development-provider install \
  --isolation-domain iso_00000000000000000001 \
  --service svc_00000000000000000001 \
  --revision rev_00000000000000000001 \
  --run-id '<gateway-run-id>' \
  --repository-root /absolute/path/to/dataground \
  --workspace-root /absolute/private/gateway-deployment \
  --docker-binary /absolute/path/to/docker \
  --supervisor-local-image-id 'sha256:<accepted-local-image-identity>' \
  --gateway-config-sha256 '<accepted-gateway-configuration-sha256>' \
  --openshell-binary /absolute/path/to/openshell \
  --credential-directory /absolute/private/provider-source/bundle \
  --actor development-operator \
  --correlation-id cor_00000000000000000001
```

The command first verifies the retained gateway request and original running process. It rejects an existing provider without its own prior creation intent. On first installation it enables and verifies the gateway's `providers_v2_enabled` setting, consumes the source, flushes immutable creation intent, and calls OpenShell with bare credential key names and an isolated child environment. It then verifies provider type, the exact four credential keys, immutable binding identity, resource version, the enabled setting, and the original gateway again. No model request or sandbox is created by this command.

Success prints one `dataground.dev.provider-deployment/v1` receipt with the isolation domain, service, revision, gateway run identifier, `providerProfile: codex`, actor, correlation, `state: configured`, and `certificationEligible: false`. The owner-only request and receipt files have the prefix `dg-runtime-deployment-<run-id>.provider`. Only the private receipt contains the native binding identity and version. These files provide local operator provenance; they do not replace PostgreSQL workload authorization and decision audit.

Use `inspect` in place of `install` with the same arguments to check the recorded deployment and binding. Exact replay never reads the credential bundle again and never repeats provider creation. A lost creation acknowledgement or receipt write can be recovered by observing the exact configured provider after the durable intent. Inspection may publish the missing private receipt. A changed request, replacement binding, changed resource version, disabled provider setting, retired gateway, or restarted gateway fails closed. A later credential refresh that changes OpenShell's resource version also requires operator reconciliation; this command does not accept a new version automatically.

If creation intent exists but no provider can be observed, retain the files and reconcile the deployment manually. The command will not repeat credential transfer. A failure before creation intent can consume the bundle; prepare a fresh temporary bundle at the same path before an exact retry. Never delete intent or receipt files to force creation. The command has a three-minute deadline; the adapter can continue cancellation-independent acknowledgement observation for up to 30 seconds afterward. It returns static errors and does not claim rollback, remove a provider, or delete gateway data. Gateway stop retains the configured provider in OpenShell's private state.

After installation, activate the separate [provider-profile grant](governed-worker.md#provider-credential-mediation), install the remaining execution inputs, and publish the exact revision. Signed runtime acceptance, effective policy, provider grants, and execution-time checks remain required. Successful configuration proves neither upstream credential validity nor the complete governed journey. Credential acquisition, refresh, rotation, detach, upstream revocation, model-routing conformance, and production administration remain separate work.

The standard CI gateway test installs synthetic credentials with the checksum-pinned OpenShell CLI, checks source consumption and exact replay, and recovers a missing receipt against the same native binding. It makes no upstream inference request and does not constitute runtime or provider credential certification.
