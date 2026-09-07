# The three Go services, in one image. Which one runs is the container's command.
#
# Two stages: a builder with the toolchain, and a runtime with nothing but the binaries and the
# certificates they need. The runtime carries no shell utilities beyond what the healthcheck uses,
# and runs as a non-root user — the signer holds key material, and an image is a place where "just
# for debugging" tools become permanent.

FROM golang:1.25-bookworm AS build
WORKDIR /src

# Dependencies first, so a source change does not re-download the module graph.
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY backend/ ./
# contracts/ is read at runtime by verify-contracts, and at build time by the tests. It lives
# outside backend/ because both runtimes are checked against it.
COPY contracts/ /contracts/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin leash

COPY --from=build /out/ /bin/
COPY --from=build /contracts/ /contracts/

# The state directory is created HERE, owned by the non-root user, because Docker copies an
# image directory's ownership when it first initialises an empty named volume. Without this the
# volume arrives owned by root and the process — which correctly does not run as root — cannot
# write the sandbox keys it is supposed to create.
RUN mkdir -p /state && chown leash:leash /state
VOLUME /state

USER leash
# Exec form, so PID 1 receives SIGTERM and the graceful shutdown actually runs.
ENTRYPOINT []
