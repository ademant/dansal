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
    -o /dansal_web ./cmd/dansal_web

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /dansal_web /usr/local/bin/dansal_web
COPY docker/defaults/web.yaml /defaults/web.yaml
COPY docker/dansal_web/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
VOLUME ["/etc/dansal", "/var/lib/dansal-web"]
EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/local/bin/dansal_web", "/etc/dansal/web.yaml"]
