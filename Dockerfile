# NexQuake Runtime Shell
# Provides runtime environment for running bind-mounted artifacts.
#
# Goal: keep the image lightweight. We use wolfi-base and keep runtime deps minimal.

FROM cgr.dev/chainguard/wolfi-base:latest

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
