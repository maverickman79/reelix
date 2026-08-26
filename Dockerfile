# Reelix server image.
#
# The runtime stage is debian:bookworm-slim rather than scratch or distroless
# on purpose. From Step 4 onward Reelix shells out to the jellyfin-ffmpeg7
# binaries, which need a real userland: apt, shared libraries, and the driver
# packages that hardware acceleration depends on. Choosing a minimal base now
# would only mean rebuilding this file later.

FROM golang:1.27-bookworm AS build

WORKDIR /src

# Dependency layer first so it is cached independently of the source. Reelix
# currently has no third-party dependencies; this stays correct when it does.
COPY go.mod ./
RUN go mod download

COPY . .

ARG VERSION=0.0.1
# CGO is off: the binary is fully static, so the runtime stage needs no Go
# toolchain or libc coupling to the builder.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/reelixd ./cmd/reelixd


FROM debian:bookworm-slim AS runtime

# ca-certificates for outbound TLS (metadata providers, later steps).
# curl backs the compose healthcheck.
# gnupg is needed only to dearmor the Jellyfin signing key below.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        gnupg \
    && rm -rf /var/lib/apt/lists/*

# jellyfin-ffmpeg7 provides ffmpeg and ffprobe.
#
# Reelix does not build its own FFmpeg: these builds already carry the QSV,
# NVENC, and VA-API patches and driver plumbing that make hardware
# acceleration work across the hardware Reelix targets. They are invoked as
# subprocesses, never linked and never cgo-bound, which keeps their separate
# licensing and distribution cleanly separated from this AGPL binary.
#
# Pinned to the 7.x series deliberately. The repository also carries
# jellyfin-ffmpeg5, 6, and 8; an unpinned install would silently change the
# encoder behaviour under the playback decisions in Step 7.
RUN install -d -m 0755 /etc/apt/keyrings \
    && curl -fsSL https://repo.jellyfin.org/jellyfin_team.gpg.key \
        | gpg --dearmor -o /etc/apt/keyrings/jellyfin.gpg \
    && chmod 0644 /etc/apt/keyrings/jellyfin.gpg \
    && echo "deb [signed-by=/etc/apt/keyrings/jellyfin.gpg arch=amd64] https://repo.jellyfin.org/debian bookworm main" \
        > /etc/apt/sources.list.d/jellyfin.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends jellyfin-ffmpeg7 \
    && rm -rf /var/lib/apt/lists/* \
    && /usr/lib/jellyfin-ffmpeg/ffprobe -version | head -1

# Run unprivileged. UID/GID 1000 matches the common desktop-Linux first user,
# which keeps bind-mounted media and config readable without chowning them.
RUN groupadd --gid 1000 reelix \
    && useradd --uid 1000 --gid 1000 --home-dir /var/lib/reelix --create-home reelix

COPY --from=build /out/reelixd /usr/local/bin/reelixd

# Persistent state lives on volumes, outside the image.
RUN mkdir -p /config /cache && chown reelix:reelix /config /cache
VOLUME ["/config", "/cache"]

USER reelix

ENV REELIX_HTTP_ADDR=:8080 \
    REELIX_CONFIG_DIR=/config \
    REELIX_CACHE_DIR=/cache \
    REELIX_FFPROBE_PATH=/usr/lib/jellyfin-ffmpeg/ffprobe \
    REELIX_FFMPEG_PATH=/usr/lib/jellyfin-ffmpeg/ffmpeg

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/reelixd"]
