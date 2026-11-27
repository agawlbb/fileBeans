# Prepared ARM64 binaries only. Build on VM-laptop, never on the Pi.
FROM ghcr.io/home-assistant/aarch64-base:3.24-2026.11.0
RUN apk add --no-cache fuse3 util-linux su-exec
COPY filebeans filebeans-vault-broker gocryptfs rclone /usr/local/bin/
COPY limits.sh /etc/cont-init.d/00-filebeans-limits.sh
COPY broker.run /etc/services.d/filebeans-broker/run
COPY api.run /etc/services.d/filebeans-api/run
RUN chmod 0555 /usr/local/bin/filebeans /usr/local/bin/filebeans-vault-broker /usr/local/bin/gocryptfs /usr/local/bin/rclone /etc/cont-init.d/00-filebeans-limits.sh /etc/services.d/filebeans-broker/run /etc/services.d/filebeans-api/run
