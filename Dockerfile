# WebQuake Runtime Shell
# Provides runtime environment for testing artifacts
#
# Usage:
#   1. Download PR artifacts from GitHub Actions
#   2. Extract to ./apps/ directory
#   3. docker compose up

FROM ubuntu:22.04

RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    if [ "$arch" = "arm64" ]; then \
      dpkg --add-architecture armhf; \
    fi; \
    apt-get update; \
    pkgs="ca-certificates curl bash"; \
    if [ "$arch" = "amd64" ]; then \
      pkgs="$pkgs libc6-x32 libx32gcc-s1"; \
    elif [ "$arch" = "arm64" ]; then \
      pkgs="$pkgs libc6:armhf libgcc-s1:armhf"; \
    fi; \
    apt-get install -y --no-install-recommends $pkgs; \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Create directories for bind mounts
RUN mkdir -p /apps /data /logs

ENV HTTP_PORT=7071 \
    QUAKE_DATA_DIR=/data \
    LOGS_DIR=/logs \
    CLIENT_DIR=/apps/nqwasm \
    SERVER_BINARY=/apps/nqserver

EXPOSE 7071

CMD ["/apps/nexus"]
