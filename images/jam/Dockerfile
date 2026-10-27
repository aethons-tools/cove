# The Jam image: built FROM already-built release binaries (scripts/jam-image.sh
# stages them into bin/linux-<arch>/), not compiled here. Never runs as root;
# holds no docker, ssh or git binary.
FROM alpine:3.22

ARG TARGETARCH

RUN apk add --no-cache ca-certificates
RUN adduser -D -u 10001 -h /var/lib/jam jam && mkdir -p /etc/jam && chmod 755 /etc/jam

COPY bin/linux-${TARGETARCH}/ /usr/local/bin/

ENV HOME=/var/lib/jam
USER jam
WORKDIR /var/lib/jam
VOLUME /var/lib/jam
EXPOSE 8443 8081

ENTRYPOINT ["at-jam"]
CMD ["serve","--config","/etc/jam/jam.yml"]
