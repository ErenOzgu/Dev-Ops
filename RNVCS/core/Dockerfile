# syntax=docker/dockerfile:1

# RNVCS core (Yönetim Servisi) — küçük, root olmayan imaj.
# Derleme bağlamı: RNVCS/core   (docker build -t rnvcs-core RNVCS/core)
ARG GO_VERSION=1.23
ARG ALPINE_VERSION=3.20

FROM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS build
WORKDIR /src
# Harici bağımlılık yok (go.sum yok): yalnızca standart kütüphane.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/rnvcs-core .

FROM alpine:${ALPINE_VERSION}
# core veritabanına psql istemcisi üzerinden bağlanır (bkz. internal/pg).
RUN apk add --no-cache postgresql16-client ca-certificates tzdata \
 && addgroup -S rnvcs \
 && adduser -S -G rnvcs -H -h /nonexistent rnvcs
COPY --from=build /out/rnvcs-core /usr/local/bin/rnvcs-core

LABEL org.opencontainers.image.title="rnvcs-core" \
      org.opencontainers.image.description="RNVCS management service: REST API, Asterisk config writers, maintenance terminal" \
      org.opencontainers.image.source="https://github.com/ErenOzgu/Dev-Ops"

USER rnvcs
EXPOSE 8091
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
  CMD wget -q -O /dev/null http://127.0.0.1:8091/healthz || exit 1
ENTRYPOINT ["rnvcs-core"]
