# syntax=docker/dockerfile:1.7

# NexQuake production image.
# Builds Nexus + dedicated server + WASM client from source (no bind-mounted artifacts).

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
ARG NQ_GO_BUILD_P=
RUN --mount=type=cache,target=/root/.cache/go-build \
	set -eu; \
	go_build_p="${NQ_GO_BUILD_P:-}"; \
	if [ -z "${go_build_p}" ]; then \
		if command -v nproc >/dev/null 2>&1; then \
			go_build_p="$(nproc)"; \
		else \
			go_build_p="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)"; \
		fi; \
	fi; \
	case "${go_build_p}" in \
		''|*[!0-9]*|0) echo "error: NQ_GO_BUILD_P must be a positive integer (got '${go_build_p}')" >&2; exit 2 ;; \
	esac; \
	CGO_ENABLED=0 go build -p "${go_build_p}" -trimpath -ldflags "-s -w -X github.com/brstm/NexQuake/nexus.gitSHA=${GIT_SHA} -X github.com/brstm/NexQuake/nexus.buildTime=${BUILD_TIME}" -o /out/nexus .

FROM scratch AS nexus-artifact
COPY --from=nexus-builder /out/nexus /nexus

FROM cgr.dev/chainguard/wolfi-base:latest AS server-builder
WORKDIR /src

RUN apk add --no-cache gcc make glibc-dev git patch ca-certificates bash

COPY build/ build/
COPY bugfix/ bugfix/
COPY server/ server/

ARG TARGETPLATFORM
RUN set -eu; \
	mkdir -p /out; \
	OUT=/out/nqserver PLATFORM="${TARGETPLATFORM:-}" ./build/build-server.sh

FROM scratch AS server-artifact
COPY --from=server-builder /out/nqserver /nqserver

# CI-optimized server artifact build:
# expects src/build/tmp/WinQuake to be pre-fetched in build context, so git is not
# required inside the container.
FROM cgr.dev/chainguard/wolfi-base:latest AS server-builder-ci
WORKDIR /src

RUN apk add --no-cache gcc make glibc-dev patch ca-certificates bash

COPY build/ build/
COPY build/tmp/WinQuake build/tmp/WinQuake
COPY bugfix/ bugfix/
COPY server/ server/

ARG TARGETPLATFORM
RUN set -eu; \
	mkdir -p /out; \
	OUT=/out/nqserver PLATFORM="${TARGETPLATFORM:-}" ./build/build-server.sh

FROM scratch AS server-artifact-ci
COPY --from=server-builder-ci /out/nqserver /nqserver

FROM ${EMSDK_IMAGE} AS wasm-builder
WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends git make patch ca-certificates bash && rm -rf /var/lib/apt/lists/*

COPY build/ build/
COPY bugfix/ bugfix/
COPY client/ client/
COPY etc/ etc/
ARG NQ_VERSION=dev

RUN set -eu; \
	printf '%s\n' "${NQ_VERSION}" > /VERSION; \
	OUT_DIR=/out/nqwasm ./build/build-client.sh

FROM scratch AS wasm-artifact
COPY --from=wasm-builder /out/nqwasm/ /

FROM ${WOLFI_BASE_IMAGE}
WORKDIR /app

RUN mkdir -p ./bin/nqwasm ./server ./game ./logs ./etc
COPY etc/ ./etc

COPY --from=nexus-builder /out/nexus ./bin/nexus
COPY --from=server-builder /out/nqserver ./bin/nqserver
COPY --from=wasm-builder /out/nqwasm ./bin/nqwasm

EXPOSE 1337
ENTRYPOINT ["/app/bin/nexus"]
