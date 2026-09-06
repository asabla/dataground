# Experimental strict-Landlock compatibility candidate; never a default profile.
FROM rust:1.95.0-alpine3.22@sha256:064dfc925d68d1a63f4fd2871bd7dc6e6ea56692989a487185855d62885d90aa AS build
ARG TARGETARCH
RUN apk add --no-cache build-base clang clang-dev cmake git linux-headers openssl-dev openssl-libs-static pkgconf protobuf-dev python3
WORKDIR /source
RUN git init . && git remote add origin https://github.com/NVIDIA/OpenShell.git && git fetch --depth=1 origin d556748771c41cbbd4e4dd7cd9030c798afe2b7d && git checkout --detach FETCH_HEAD
COPY Dockerfile source.json strict-file-rights.patch prepare-source.py prepare-source_test.py /candidate/
RUN PYTHONDONTWRITEBYTECODE=1 python3 /candidate/prepare-source_test.py /source && python3 /candidate/prepare-source.py /source
ENV CARGO_PROFILE_DEV_DEBUG=0 CARGO_INCREMENTAL=0 CARGO_BUILD_JOBS=4 OPENSSL_STATIC=1
RUN --mount=type=cache,id=dataground-supervisor-cargo,sharing=locked,target=/usr/local/cargo/registry \
    --mount=type=cache,id=dataground-supervisor-target-${TARGETARCH},sharing=locked,target=/source/target \
    cargo +1.95.0 test --locked -p openshell-supervisor-process --lib dataground_strict -- --nocapture
RUN --mount=type=cache,id=dataground-supervisor-cargo,sharing=locked,target=/usr/local/cargo/registry \
    --mount=type=cache,id=dataground-supervisor-target-${TARGETARCH},sharing=locked,target=/source/target \
    cargo +1.95.0 build --locked -p openshell-sandbox --bin openshell-sandbox && \
    mkdir /output && cp target/debug/openshell-sandbox /output/openshell-sandbox
RUN apk info -vv > /source/build-packages.txt && sha256sum /output/openshell-sandbox > /source/candidate-binary.sha256

FROM ghcr.io/nvidia/openshell/supervisor@sha256:a15222ac18c1afd0ee51b9dda785a29067c13f61a2002a29d41f691f5e817f19
COPY --from=build --chmod=0555 /output/openshell-sandbox /openshell-sandbox
COPY --from=build /source/LICENSE /source/candidate-source.json /source/build-packages.txt /source/candidate-binary.sha256 /usr/local/share/dataground-supervisor-compatibility/
LABEL org.opencontainers.image.source="https://github.com/asabla/dataground" \
      dataground.dev.supervisor-compatibility-source="d556748771c41cbbd4e4dd7cd9030c798afe2b7d" \
      dataground.dev.certification-eligible="false"
