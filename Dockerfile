# syntax=docker/dockerfile:1.7

FROM golang:1.25-bookworm AS builder

ARG MEDBEADS_PROJECTION_VERSION=paper-demo-v1

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential ca-certificates libsqlite3-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=1 go build \
    -buildvcs=false \
    -tags sqlite_fts5 \
    -trimpath \
    -ldflags='-s -w' \
    -o /out/medbeadsd ./cmd/medbeadsd

# The public image contains only synthetic Pod files. index.db and every
# derived clinical link are rebuilt inside the image from those immutable
# sources, so the repository does not ship a platform-created SQLite file.
COPY demo_data/pods /seed/pods
RUN /out/medbeadsd verify -data /seed \
    && /out/medbeadsd reindex -data /seed \
    && /out/medbeadsd reproject \
       -data /seed \
       -code-version "${MEDBEADS_PROJECTION_VERSION}" \
       -record-state \
       -drain \
    && /out/medbeadsd verify -data /seed \
    && rm -f /seed/.medbeads.lock /seed/index.db-shm /seed/index.db-wal

FROM debian:bookworm-slim AS paper-demo

ARG MEDBEADS_PROJECTION_VERSION=paper-demo-v1

LABEL org.opencontainers.image.title="MedBeads paper demo" \
      org.opencontainers.image.description="Synthetic 10-patient reference implementation; not for clinical use" \
      org.opencontainers.image.source="https://github.com/medbeads/medbeads" \
      org.opencontainers.image.licenses="Apache-2.0" \
      io.medbeads.distribution-profile="public-paper-demo"

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/medbeadsd /usr/local/bin/medbeadsd
COPY --from=builder --chown=65532:65532 /seed /data

ENV HOME=/tmp \
    MEDBEADS_DISTRIBUTION_PROFILE=public-paper-demo \
    MEDBEADS_PROJECTION_VERSION=${MEDBEADS_PROJECTION_VERSION}

USER 65532:65532
WORKDIR /data

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/medbeadsd"]
CMD ["serve", "-data", "/data", "-role", "viewer", "-http", "0.0.0.0:8080", "-projection-code-version", "paper-demo-v1"]
