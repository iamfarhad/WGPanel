FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/. .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wgpanel-agent ./cmd/agent

# AmneziaWG (obfuscated WireGuard), built from source and pinned to a commit so a node
# image is reproducible and a moved upstream tag can't change it silently. The panel's
# parameter validation (backend/internal/obfuscation) follows exactly these versions:
#   amneziawg-go    v0.2.19          AWG 2.0 userspace implementation (S1-S4, H ranges, I1-I5)
#   amneziawg-tools v1.0.20260618    awg / awg-quick (parses the AWG 2.0 keys)
# v0.2.x is deliberately chosen over the newer v3.x line: v3 changes the wire format
# options (header protection key, content padding) that stock AWG client apps don't
# speak yet, while v0.2.19 is what AmneziaVPN / AmneziaWG apps interoperate with.
# Bumping either means re-reading device/uapi.go and obf*.go for changed limits.
FROM golang:1.26-alpine AS awg-go
RUN apk add --no-cache git
ARG AWG_GO_COMMIT=1cc94272ca8e9e223a5fe76382f5880f09d3c12d
RUN git clone https://github.com/amnezia-vpn/amneziawg-go.git /awg-go && \
    cd /awg-go && git checkout --detach "${AWG_GO_COMMIT}"
WORKDIR /awg-go
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/amneziawg-go .

# The tools link against glibc, so they are built on the same Debian release as the
# runtime stage (an alpine/musl build would not run there).
FROM debian:bookworm-slim AS awg-tools
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates build-essential && \
    rm -rf /var/lib/apt/lists/*
ARG AWG_TOOLS_COMMIT=4cdc357c68b39a0d1e19417b7f03d604c1e1b4cf
RUN git clone https://github.com/amnezia-vpn/amneziawg-tools.git /awg-tools && \
    cd /awg-tools && git checkout --detach "${AWG_TOOLS_COMMIT}" && \
    make -C src -j"$(nproc)" && \
    install -D -m 0755 src/wg /out/awg && \
    install -D -m 0755 src/wg-quick/linux.bash /out/awg-quick

# Debian (not distroless, unlike backend/Dockerfile's api image) - this container
# needs a real userland: wg-quick is a bash script, plus iptables/iproute2 for the
# NAT/forwarding setup install-node.sh normally does on a bare Linux host. The
# iptables package ships the xtables extensions node-entrypoint.sh uses (nat
# MASQUERADE and the mangle TCPMSS clamp, for IPv4 and IPv6); the kernel modules come
# from the host. Plain WireGuard stays the default (kernel module, wg-quick); the awg
# binaries below are only used when the panel assigns the node an obfuscation profile,
# and need /dev/net/tun for the userspace data path.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      wireguard-tools iptables iproute2 ca-certificates && \
    rm -rf /var/lib/apt/lists/*
COPY --from=awg-go /out/amneziawg-go /usr/local/bin/amneziawg-go
COPY --from=awg-tools /out/awg /usr/local/bin/awg
COPY --from=awg-tools /out/awg-quick /usr/local/bin/awg-quick
COPY --from=build /out/wgpanel-agent /usr/local/bin/wgpanel-agent
COPY deploy/node-entrypoint.sh /usr/local/bin/node-entrypoint.sh
RUN chmod +x /usr/local/bin/node-entrypoint.sh && mkdir -p /etc/amnezia/amneziawg && chmod 700 /etc/amnezia /etc/amnezia/amneziawg
ENTRYPOINT ["/usr/local/bin/node-entrypoint.sh"]
