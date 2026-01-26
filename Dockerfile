# WebQuake Runtime Shell
# Provides runtime environment for testing artifacts
#
# Usage:
#   1. Download PR artifacts from GitHub Actions
#   2. Extract to ./apps/ directory
#   3. docker compose up

FROM ubuntu:22.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl bash && \
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
