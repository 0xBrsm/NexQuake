# syntax=docker/dockerfile:1.7

# NexQuake production image.
# Builds nexus + dedicated server + WASM client from source (no bind-mounted artifacts).

ARG WOLFI_BASE_IMAGE=cgr.dev/chainguard/wolfi-base:latest
ARG GOLANG_IMAGE=golang:1.25-alpine
ARG EMSDK_IMAGE=emscripten/emsdk:5.0.0

FROM ${GOLANG_IMAGE} AS nexus-builder
WORKDIR /src/nexus

RUN apk add --no-cache git ca-certificates

COPY nexus/go.mod nexus/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY nexus/ ./
ARG GIT_SHA=dev
ARG BUILD_TIME=
RUN --mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/brstm/NexQuake/nexus.gitSHA=${GIT_SHA} -X github.com/brstm/NexQuake/nexus.buildTime=${BUILD_TIME}" -o /out/nexus .

FROM cgr.dev/chainguard/wolfi-base:latest AS server-builder
WORKDIR /src

RUN apk add --no-cache build-base git patch ca-certificates bash

COPY build/ build/
COPY server/ server/

ARG TARGETPLATFORM
RUN set -eu; \
	mkdir -p /out; \
	OUT=/out/nqserver PLATFORM="${TARGETPLATFORM:-}" ./build/build-server.sh

FROM ${EMSDK_IMAGE} AS wasm-builder
WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends git make patch ca-certificates bash && rm -rf /var/lib/apt/lists/*

COPY build/ build/
COPY client/ client/

RUN set -eu; \
	OUT_DIR=/out/nqwasm ./build/build-client.sh

FROM ${WOLFI_BASE_IMAGE}
WORKDIR /app

RUN mkdir -p ./bin ./data ./logs
COPY manifests/ ./data

COPY --from=nexus-builder /out/nexus ./bin/nexus
COPY --from=server-builder /out/nqserver ./bin/nqserver
COPY --from=wasm-builder /out/nqwasm ./bin/nqwasm

EXPOSE 1337
ENTRYPOINT ["/app/bin/nexus"]
