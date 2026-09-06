# Experimental compatibility candidate; does not replace the development profile.
FROM rust:1.93.0-bookworm@sha256:d0a4aa3ca2e1088ac0c81690914a0d810f2eee188197034edf366ed010a2b382 AS build
RUN apt-get update && apt-get install -y --no-install-recommends libcap-dev libssl-dev pkg-config python3 && rm -rf /var/lib/apt/lists/*
WORKDIR /source
RUN git init . && git remote add origin https://github.com/openai/codex.git && git fetch --depth=1 origin 4c70bff480af37b1bf1a9b352b8341060fe55755 && git checkout --detach FETCH_HEAD
COPY Dockerfile source.json seccomp-prctl.patch prepare-source.py prepare-source_test.py /candidate/
RUN python3 /candidate/prepare-source_test.py /source && python3 /candidate/prepare-source.py /source
WORKDIR /source/codex-rs
ENV CARGO_PROFILE_DEV_DEBUG=0 CARGO_INCREMENTAL=0 CARGO_BUILD_JOBS=2
RUN cargo +1.93.0 build --locked -p codex-linux-sandbox -p codex-cli --bin codex-linux-sandbox --bin codex
RUN dpkg-query -W > /source/build-packages.txt && sha256sum target/debug/codex target/debug/codex-linux-sandbox > /source/candidate-binaries.sha256

FROM ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:aeef1c63f00e2913ea002ccb3aaf925f338b5c5d70e63576f0d95c16a138044e
COPY --from=build /source/codex-rs/target/debug/codex /opt/dataground-compatibility/codex
COPY --from=build /source/codex-rs/target/debug/codex-linux-sandbox /opt/dataground-compatibility/codex-linux-sandbox
COPY --from=build /source/LICENSE /source/NOTICE /source/candidate-source.json /source/build-packages.txt /source/candidate-binaries.sha256 /usr/local/share/dataground-codex-compatibility/
LABEL org.opencontainers.image.source="https://github.com/asabla/dataground" \
      dataground.dev.codex-compatibility-source="4c70bff480af37b1bf1a9b352b8341060fe55755" \
      dataground.dev.certification-eligible="false"
