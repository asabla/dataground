# Experimental extension of an independently pinned runtime image.
# The build context contains only this file and the cross-compiled helper.
ARG RUNTIME_IMAGE=ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:aeef1c63f00e2913ea002ccb3aaf925f338b5c5d70e63576f0d95c16a138044e
FROM ${RUNTIME_IMAGE}
USER root
COPY --chmod=0555 dataground-workspace-materialize /usr/local/bin/dataground-workspace-materialize
RUN test ! -e /sandbox/workspace && test ! -L /sandbox/workspace && \
    install -d -m 0700 -o sandbox -g sandbox /sandbox
USER sandbox
LABEL org.opencontainers.image.source="https://github.com/asabla/dataground" \
      dataground.dev.workspace-source-contract="dataground.workspace-source-materialization/v1" \
      dataground.dev.certification-eligible="false"
