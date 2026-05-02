# syntax=docker/dockerfile:1.7

# ---- builder -----------------------------------------------------------------
FROM golang:1.26.2-alpine AS builder
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev

ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}

RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/awg-api        ./cmd/awg-api && \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/awg-worker     ./cmd/awg-worker && \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/awg-node-agent ./cmd/awg-node-agent

# ---- amneziawg-tools --------------------------------------------------------
FROM alpine:3.23 AS awg-tools-builder
ARG AMNEZIAWG_TOOLS_TAG=v1.0.20260223
ARG AMNEZIAWG_TOOLS_COMMIT=5d6179a6d0842e98dfb349c28cf1bd8e4b9d1079
RUN apk add --no-cache build-base git linux-headers
RUN git clone --depth 1 --branch "${AMNEZIAWG_TOOLS_TAG}" https://github.com/amnezia-vpn/amneziawg-tools.git /src/amneziawg-tools && \
    cd /src/amneziawg-tools && \
    test "$(git rev-parse HEAD)" = "${AMNEZIAWG_TOOLS_COMMIT}" && \
    make -C src && \
    mkdir -p /out && \
    cp src/wg /out/awg && \
    cp src/wg-quick/linux.bash /out/awg-quick && \
    chmod 0755 /out/awg /out/awg-quick

# ---- api ---------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot AS awg-api
COPY --from=builder /out/awg-api /awg-api
USER nonroot:nonroot
EXPOSE 18080 9090
ENTRYPOINT ["/awg-api"]

# ---- worker ------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot AS awg-worker
COPY --from=builder /out/awg-worker /awg-worker
USER nonroot:nonroot
ENTRYPOINT ["/awg-worker"]

# ---- amneziawg-go (userspace) -----------------------------------------------
FROM golang:1.26.2-alpine AS awg-go-builder
ARG AMNEZIAWG_GO_TAG=v0.2.17
ARG AMNEZIAWG_GO_COMMIT=12a012205e3c444be02aba91a840455f74c127e1
RUN apk add --no-cache git make
RUN git clone --depth 1 --branch "${AMNEZIAWG_GO_TAG}" https://github.com/amnezia-vpn/amneziawg-go.git /src/amneziawg-go && \
    cd /src/amneziawg-go && \
    test "$(git rev-parse HEAD)" = "${AMNEZIAWG_GO_COMMIT}" && \
    make && \
    mkdir -p /out && \
    cp amneziawg-go /out/amneziawg-go
RUN chmod 0755 /out/amneziawg-go && \
    ln -s amneziawg-go /out/wireguard-go

# ---- node-agent --------------------------------------------------------------
# Ships with amneziawg-tools + amneziawg-go userspace fallback. The container
# still needs CAP_NET_ADMIN and /dev/net/tun when the kernel module is absent.
FROM alpine:3.23 AS awg-node-agent
RUN apk add --no-cache bash iproute2 iptables nftables openresolv ca-certificates
COPY --from=awg-tools-builder /out/awg /usr/local/bin/awg
COPY --from=awg-tools-builder /out/awg-quick /usr/local/bin/awg-quick
COPY --from=awg-go-builder /out/amneziawg-go /usr/local/bin/amneziawg-go
COPY --from=awg-go-builder /out/wireguard-go /usr/local/bin/wireguard-go
COPY --from=builder /out/awg-node-agent /awg-node-agent
ENV WG_QUICK_USERSPACE_IMPLEMENTATION=amneziawg-go
EXPOSE 8081
ENTRYPOINT ["/awg-node-agent"]
