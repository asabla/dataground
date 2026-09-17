# Operator-owned development gateway

`dataground-development-gateway` prepares and starts the checked host-network OpenShell gateway on a local Linux Docker host. The gateway remains running when the command exits or a worker restarts. This is an operator tool for the experimental governed development slice. It does not create a public gateway resource, grant provider access, accept runtime evidence, or supply production certification. Platform lifecycle state still belongs in PostgreSQL; these private files record the operator's local deployment request and native process pins.

Use a trusted local filesystem supported by the [deployment observer](openshell-local.md#observe-an-existing-development-gateway), a stable host clock, the local `/var/run/docker.sock`, and an existing owner-only mode-0700 workspace root outside the repository. Run the command as the same host user that will run the worker. Preload the pinned gateway image and, for the strict profile, the independently accepted supervisor image. The command never builds or pulls images and ignores ambient Docker hosts, contexts, registry configuration, and account environment. The gateway's loopback control and health ports must be available.

For a strict Linux ARM64 deployment, use the exact local supervisor image identity and gateway configuration SHA-256 from the verified v2 acceptance. Supply a fresh 32-character lower-case hexadecimal run identifier and the accepted service-revision scope. Keep the complete command arguments for replay:

```sh
go run ./cmd/dataground-development-gateway up \
  --isolation-domain iso_00000000000000000001 \
  --service svc_00000000000000000001 \
  --revision rev_00000000000000000001 \
  --run-id '<32 lower-case hex characters>' \
  --repository-root /absolute/path/to/dataground \
  --workspace-root /absolute/private/gateway-deployment \
  --docker-binary /absolute/path/to/docker \
  --supervisor-local-image-id 'sha256:<accepted-local-image-identity>' \
  --gateway-config-sha256 '<accepted-gateway-configuration-sha256>'
```

All paths must be absolute and clean. The workspace-root path must resolve to itself, without symlink aliases. The command stages the checked Compose file, the exact supervisor configuration, a fresh private gateway signing key, and a private gateway state directory under `dg-runtime-topology-<run-id>`. It checks candidate source and patch labels, then waits for the configuration to predate process startup by the observer's required timestamp gap. It uses only the exact Compose project and disables recreation, builds, and pulls. The worker remains responsible for verifying the published candidate signatures, signed acceptance, service scope, provider grants, and enforcement policy before execution.

The stock checked supervisor can also be prepared on a Linux host by omitting `--supervisor-local-image-id` and supplying the checked `deploy/openshell/runtime-conformance/gateway.toml` SHA-256. This does not satisfy the strict worker profile, which requires the accepted candidate identity.

On success, stdout contains one `dataground.dev.gateway-deployment/v1` JSON receipt. Its fields are `contract`, `isolationDomainId`, `serviceId`, `revisionId`, `runId`, `containerId`, `startedAt`, `supervisorLocalImageId`, `gatewayConfigSHA256`, and `state`. The initial state is `running`. Use its exact `runId`, `containerId`, and `startedAt` for the strict worker's independent deployment pins; retain the same workspace root and Docker binary. The receipt is an observation of an operator-owned deployment, not a signed runtime acceptance. Review it with the accepted v2 evidence before configuring the worker.

Run the same command with `inspect` in place of `up` to verify the original process, image, mounted configuration, and listeners. Repeating `up` with the exact arguments has the same observation behavior after startup. A changed scope, path, image, or digest fails before further native operations. A replaced or restarted container cannot update an existing receipt. Each command holds one nonblocking per-run lock; a second operator command must retry after the current command exits.

The workspace root retains owner-only immutable request, start-intent, and receipt files with the prefix `dg-runtime-deployment-<run-id>`, plus a stable lock file. Start intent is flushed before Docker is called. If the command loses the Docker acknowledgement or exits before publishing its receipt, repeating `up` observes the exact existing project and publishes a receipt only after all deployment checks pass. It never repeats the native start after that intent. If the crash occurred before Docker took effect, or the partial deployment cannot pass observation, retain the files and reconcile the exact project manually before using a new run identifier. Do not delete intent or lock files to force a retry. A partial workspace is never overwritten, and unavailable Docker inspection is never treated as absence.

After draining work and stopping the worker, run the same command with `stop` in place of `up`. It writes a permanent stop intent, verifies the exact recorded container and original start timestamp, stops that container, and confirms that it is stopped. A lost stop acknowledgement is resolved by observation; an exact replay is safe. The returned receipt has state `stopped`. Further `up` or `inspect` commands reject the retired deployment. Stop does not delete the container, signing keys, gateway database, deployment records, or sandbox data, and it does not stop arbitrary child sandboxes. The operator owns subsequent retention and disposal after reconciliation. The command rejects a replacement or restart observed before stop. Docker stop addresses the immutable container ID and cannot atomically compare the process start timestamp. The trusted operator must exclude concurrent direct Docker administration during this operation; the per-run lock serializes this command only.

The command has a three-minute deadline and returns static errors without native output. Failure does not assert rollback or trigger automatic cleanup. Worker observation and worker shutdown remain read-only with respect to this deployment. The [provider command](development-provider.md) can configure the fixed Codex binding on this retained gateway. Signed acceptance, execution-plan installation, and the complete governed service journey remain separate requirements.
