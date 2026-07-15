# Digest-pinned, matching what opentelemetry-collector-releases pins today.
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS certs
RUN apk --update add ca-certificates

FROM scratch

ARG USER_UID=10001
ARG USER_GID=10001

COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=755 _build/fixter-collector /fixter-collector

USER ${USER_UID}:${USER_GID}
ENTRYPOINT ["/fixter-collector"]
EXPOSE 4317 4318 13133
