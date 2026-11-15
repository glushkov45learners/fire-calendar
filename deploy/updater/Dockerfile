# Daily data refresh job: FIRMS NRT download -> ETL (Go) -> harmonize (Rust).
# Build from the repo root:  docker build -f deploy/updater/Dockerfile -t voloshmiak/fire-calendar-updater:0.1.0 .
# Run through deploy/update_data.sh, which also loads the result into PostGIS.

FROM golang:1.27-alpine AS etl
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/etl ./cmd/etl

FROM rust:1-slim AS harmonize
ARG ALGORITHM_REPO=https://github.com/glushkov45learners/fire-calendar-algorithm.git
ARG ALGORITHM_REF=main
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch "$ALGORITHM_REF" "$ALGORITHM_REPO" /src
WORKDIR /src
RUN cargo build --release --bins

FROM debian:12-slim
RUN apt-get update && apt-get install -y --no-install-recommends bash curl ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=etl /out/etl /usr/local/bin/etl
COPY --from=harmonize /src/target/release/harmonize /src/target/release/check /usr/local/bin/
COPY deploy/updater/update.sh /usr/local/bin/update
ENTRYPOINT ["/usr/local/bin/update"]
