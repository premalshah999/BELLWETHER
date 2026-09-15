# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# 1. Build the frontend.
#
# Done first and in its own stage so that a change to Go source does not
# invalidate the npm install layer, which is by far the slowest step.
# ---------------------------------------------------------------------------
FROM node:24-alpine AS web

WORKDIR /build/web

# Dependencies are copied on their own so this layer is cached until the
# manifest actually changes.
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY web/ ./
RUN npm run build


# ---------------------------------------------------------------------------
# 2. Build the Go binary, embedding the frontend built above.
# ---------------------------------------------------------------------------
FROM golang:1.27-alpine AS build

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/
COPY --from=web /build/web/dist ./web/dist

ARG VERSION=docker

# CGO is off because modernc.org/sqlite is pure Go: the result is a static
# binary that runs on an image with no libc at all.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/tradesys ./cmd/tradesys


# ---------------------------------------------------------------------------
# 3. Runtime.
#
# Nothing but the binary, CA certificates for outbound HTTPS, and a data
# directory. The timezone database is compiled into the binary.
# ---------------------------------------------------------------------------
FROM alpine:3.21

RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 -h /data tradesys \
    && mkdir -p /data \
    && chown tradesys:tradesys /data

COPY --from=build /out/tradesys /usr/local/bin/tradesys

# Runs unprivileged: this process has no reason to be root, and it is exposed
# to the operator's network.
USER tradesys
WORKDIR /data

ENV HTTP_ADDR=:8080 \
    DB_PATH=/data/tradesys.db

EXPOSE 8080

# The health endpoint needs credentials, so the check hits the frontend shell
# instead: it proves the process is listening and serving, which is what a
# restart policy needs to know.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/ || exit 1

ENTRYPOINT ["/usr/local/bin/tradesys"]
