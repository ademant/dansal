FROM golang:1.26-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
ARG BUILD_TIME=unknown
RUN go build \
    -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}" \
    -trimpath -buildvcs=false \
    -o /dansal ./cmd/dansal

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /dansal /usr/local/bin/dansal
COPY docker/defaults/config.yaml /defaults/config.yaml
COPY docker/dansal/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
VOLUME ["/etc/dansal", "/var/lib/dansal"]
EXPOSE 8000
ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/local/bin/dansal", "--config", "/etc/dansal/config.yaml"]
