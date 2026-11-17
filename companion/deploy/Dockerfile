# Runtime-only image: compile Go and prepare pinned rclone inside development.
FROM scratch
COPY filebeans /filebeans
COPY rclone /rclone
COPY rclone-README.txt /usr/share/doc/rclone/README.txt
COPY etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 1000:1000
ENTRYPOINT ["/filebeans"]
CMD ["-config", "/config/config.json"]
