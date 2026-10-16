# Test-only extension; no publication or runtime-acceptance contract.
ARG RUNTIME_IMAGE
FROM ${RUNTIME_IMAGE}
USER root
COPY --chmod=0555 dataground-workspace-export /usr/local/bin/dataground-workspace-export
USER sandbox
LABEL dataground.dev.workspace-change-fixture="true"
