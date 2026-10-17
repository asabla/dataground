# Extension of the source-capable image from the same reviewed revision.
ARG RUNTIME_IMAGE
FROM ${RUNTIME_IMAGE}
ARG DATAGROUND_SOURCE_REVISION
USER root
COPY --chmod=0555 dataground-workspace-export dataground-native-owner /usr/local/bin/
USER sandbox
LABEL dataground.dev.workspace-change-contract="dataground.workspace-changes/v1" \
      dataground.dev.native-owner-contract="dataground.native-owner/v1" \
      dataground.dev.workspace-export-revision="${DATAGROUND_SOURCE_REVISION}" \
      dataground.dev.certification-eligible="false"
