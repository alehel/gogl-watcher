# ---- frontend ----
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- backend ----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/alehel/gogl-watcher/internal/config.Version=${VERSION}" \
    -o /out/gogl-watcher ./cmd/gogl-watcher

# ---- runtime ----
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -g 1000 gogl && adduser -D -u 1000 -G gogl gogl && \
    mkdir -p /data /library && chown gogl:gogl /data /library
COPY --from=build /out/gogl-watcher /usr/local/bin/gogl-watcher
ENV DATA_DIR=/data \
    LIBRARY_DIR=/library \
    PORT=8080 \
    LOG_LEVEL=info
VOLUME ["/data", "/library"]
EXPOSE 8080
USER gogl
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:8080/api/status >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/gogl-watcher"]
