#!/usr/bin/env bash
set -euxo pipefail

# Required env args
# TARGETARCH
# JDK_VERSION

# Map TARGETARCH to the labels the Adoptium API uses.
case "${TARGETARCH}" in
  arm64) ADOPT_ARCH="aarch64" ;;
  amd64) ADOPT_ARCH="x64" ;;
  *) echo "Unsupported arch: ${TARGETARCH}" >&2; exit 1 ;;
esac
echo "Detected arch: ${TARGETARCH} (adoptium=${ADOPT_ARCH})"

# ---------------------------------------------------------------------------
# JDK ${JDK_VERSION} (Eclipse Temurin) — bootstraps Gradle AND satisfies
# the project's jvmToolchain(25), so Gradle does not need to download a
# toolchain JDK through the locked-down proxy at runtime. (The runtime egress
# allow-list has no JDK vendor host, and foojay's Disco API is blocked, so the
# toolchain MUST be baked in here — Gradle auto-detects /opt/jdk because it is
# the JVM running Gradle, and jvmToolchain(25) matches it exactly.)
# ---------------------------------------------------------------------------
echo "Installing Eclipse Temurin JDK ${JDK_VERSION} (${ADOPT_ARCH})"
JDK_URL="https://api.adoptium.net/v3/binary/latest/${JDK_VERSION}/ga/linux/${ADOPT_ARCH}/jdk/hotspot/normal/eclipse"
TMP_JDK="$(mktemp)"
curl -fsSL "$JDK_URL" -o "$TMP_JDK"
mkdir -p /opt/jdk
tar -C /opt/jdk -xzf "$TMP_JDK" --strip-components=1
rm -f "$TMP_JDK"

# Make java/javac/keytool findable on the Dockerfile-managed PATH, which
# includes /usr/local/bin (it does NOT include /opt/jdk/bin).
for bin in /opt/jdk/bin/*; do
  ln -sf "$bin" "/usr/local/bin/$(basename "$bin")"
done

# Gradle reads JAVA_HOME from the environment. The Dockerfile rewrites PATH in
# /etc/environment after this script, but leaves JAVA_HOME alone, so this line
# survives. Also export it for interactive shells.
JAVA_HOME=/opt/jdk # TODO this must show up somewhere to be durable

echo "Java version: $(/opt/jdk/bin/java -version)"
