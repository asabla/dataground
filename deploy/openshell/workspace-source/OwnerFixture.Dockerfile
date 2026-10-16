# Test-only extension; no publication or runtime-acceptance contract.
ARG RUNTIME_IMAGE
FROM ${RUNTIME_IMAGE}
USER root
COPY --chmod=0555 dataground-process-owner-test /usr/local/bin/dataground-process-owner-test
USER sandbox
LABEL dataground.dev.process-owner-fixture="true"
