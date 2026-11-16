# Runtime-only image: compile Go in development, then package the static binary.
FROM scratch
COPY filebeans /filebeans
COPY etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 1000:1000
ENTRYPOINT ["/filebeans"]
CMD ["-config", "/config/config.json"]
