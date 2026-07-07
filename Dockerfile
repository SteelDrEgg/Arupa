# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src

RUN apk add --no-cache ca-certificates make

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    make build VERSION=$VERSION DIST_DIR=/out

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && addgroup -S arupa \
    && adduser -S -G arupa -h /data arupa \
    && mkdir -p /data/plugins /data/tmp \
    && chown -R arupa:arupa /data

COPY --from=build /out/arupa /usr/local/bin/arupa

USER arupa
WORKDIR /data

EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/arupa"]
CMD ["-config", "/data/config.toml"]
