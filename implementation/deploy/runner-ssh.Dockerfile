# Demo deployment jobs need an SSH client in addition to the normal Bash runner.
# Keep the base image digest pinned; the runner uses the resulting local image ID.
FROM ubuntu@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates openssh-client tar gzip \
    && rm -rf /var/lib/apt/lists/*
