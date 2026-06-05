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
    -o /dansal_webmin ./cmd/dansal_webmin

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /dansal_webmin /usr/local/bin/dansal_webmin
COPY docker/defaults/webmin.yaml /defaults/webmin.yaml
COPY docker/dansal_webmin/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
VOLUME ["/etc/dansal", "/var/lib/dansal-web"]
EXPOSE 8090
ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/local/bin/dansal_webmin", "--config", "/etc/dansal/webmin.yaml"]
