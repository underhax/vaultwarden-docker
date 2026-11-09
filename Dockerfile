FROM ghcr.io/dani-garcia/vaultwarden:1.37.5-alpine@sha256:bbe684a02d6b0db54256ff88eac9754f4519284a7b26314afc241fd7d0918191 AS vaultwarden-donor

FROM golang:1.27.3-alpine3.24@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS go-builder

WORKDIR /build

COPY go.mod ./
COPY init/ ./init/
COPY probe/ ./probe/

COPY --from=vaultwarden-donor /web-vault /rootfs/usr/share/vaultwarden/web-vault

RUN mkdir -p /out/data /out/var/log/vaultwarden /out/var/run/postgresql /out/etc/ssl/certs /out/usr/local/bin && \
    chmod 0755 /out/etc /out/etc/ssl /out/etc/ssl/certs && \
    chmod 0755 /out/var /out/var/log /out/var/log/vaultwarden /out/var/run /out/var/run/postgresql && \
    chmod 0755 /out/usr /out/usr/local /out/usr/local/bin && \
    chmod 0700 /out/data && \
    cp /etc/ssl/certs/ca-certificates.crt /out/etc/ssl/certs/ca-certificates.crt && \
    chmod 0444 /out/etc/ssl/certs/ca-certificates.crt && \
    find /rootfs/usr/share/vaultwarden/web-vault -type d -exec chmod 0555 {} + && \
    find /rootfs/usr/share/vaultwarden/web-vault -type f -exec chmod 0444 {} + && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/usr/local/bin/init ./init && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/usr/local/bin/probe ./probe

FROM scratch AS server

COPY --from=go-builder /out/etc /etc
COPY --from=go-builder /out/var /var
COPY --from=go-builder --chmod=0700 /out/data /data
COPY --from=go-builder /rootfs/usr/share/vaultwarden/web-vault /usr/share/vaultwarden/web-vault
COPY --from=vaultwarden-donor --chmod=0555 /vaultwarden /usr/local/bin/vaultwarden
COPY --from=go-builder --chmod=0555 /out/usr/local/bin/init /usr/local/bin/init
COPY --from=go-builder --chmod=0555 /out/usr/local/bin/probe /usr/local/bin/probe

ENV HOME=/tmp \
    LOG_FILE="/var/log/vaultwarden/vaultwarden.log"

ENTRYPOINT ["/usr/local/bin/init"]
