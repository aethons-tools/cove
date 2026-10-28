# The Jam image: built FROM already-built release binaries (scripts/jam-image.sh
# stages them into bin/linux-<arch>/), not compiled here. Never runs as root;
# holds no docker, ssh or git binary.
FROM alpine:3.24

LABEL org.opencontainers.image.source="https://github.com/aethons-tools/cove"

ARG TARGETARCH

# Alpine package versions are not pinnable across the rolling 3.22 index; the base tag is pinned.
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates
RUN addgroup -g 10001 jam && adduser -D -u 10001 -G jam -h /var/lib/jam jam && mkdir -p /var/lib/jam /etc/jam && chown 10001:10001 /var/lib/jam && chmod 755 /etc/jam
RUN test "$(id -u jam):$(id -g jam)" = 10001:10001

COPY bin/linux-${TARGETARCH}/ /usr/local/bin/

ENV HOME=/var/lib/jam
# The user is created with a fixed uid/gid 10001 above and asserted by the RUN test.
# hadolint ignore=DL3066
USER jam
WORKDIR /var/lib/jam
VOLUME /var/lib/jam
EXPOSE 8443 8081

ENTRYPOINT ["jam"]
CMD ["serve","--config","/etc/jam/jam.yml"]
