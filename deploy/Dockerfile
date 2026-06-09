# syntax=docker/dockerfile:1

# --- build stage -----------------------------------------------------------
FROM golang:1.23 AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
ARG VERSION=dev
# Static, stripped binary; no cgo so it runs on distroless/scratch.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/voujr ./cmd/voujr

# --- runtime stage ---------------------------------------------------------
# Distroless, non-root, no shell, read-only root filesystem at deploy time.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/voujr /usr/local/bin/voujr
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/voujr"]
# In controller mode the chart passes: ["--in-cluster", "--mode", "read-only"]
