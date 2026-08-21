# Digest-pinned, matching what opentelemetry-collector-releases pins today.
#
# Pinned to $BUILDPLATFORM on purpose: this stage exists only to produce
# ca-certificates.crt, which is arch-independent. Without the pin, buildx would
# run `apk add` once per TARGET platform and drag QEMU emulation into every
# non-host arch of a multi-arch build for no benefit.
FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS certs
RUN apk --update add ca-certificates

FROM scratch

ARG USER_UID=10001
ARG USER_GID=10001

# Supplied automatically by buildx, once per target platform. scripts/build.sh
# lays the binaries out under _build/linux_<arch>/ to match.
ARG TARGETARCH

COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=755 _build/linux_${TARGETARCH}/fixter-collector /fixter-collector

USER ${USER_UID}:${USER_GID}
ENTRYPOINT ["/fixter-collector"]
EXPOSE 4317 4318 13133
