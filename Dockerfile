FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
RUN go mod download
COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/rfancn/file-transfer/internal/version.Version=${VERSION}" -o /out/ft-server ./cmd/server

FROM alpine:3

RUN apk add --no-cache ca-certificates tzdata \
 && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
 && echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app
COPY --from=builder /out/ft-server /app/ft-server
COPY config/ft-server.example.toml /app/ft-server.toml.default
COPY script/docker-entrypoint.sh /app/entrypoint.sh

RUN chmod +x /app/ft-server /app/entrypoint.sh \
 && mkdir -p /app/secrets /etc/file-transfer /var/log/file-transfer

ENV PATH="/app:${PATH}"

EXPOSE 8080
ENTRYPOINT ["/app/entrypoint.sh"]
