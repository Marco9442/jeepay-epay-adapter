# 生产镜像只含 adapter。mock 目标仅用于开发堆栈。
FROM golang:1.24-alpine AS build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,https://goproxy.cn,direct
ENV CGO_ENABLED=0 GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/adapter ./cmd/adapter

FROM build AS build-mocks
RUN go build -trimpath -ldflags="-s -w" -o /out/mock-jeepay ./cmd/mockjeepay \
 && go build -trimpath -ldflags="-s -w" -o /out/mock-newapi ./cmd/mocknewapi

FROM alpine:3.21 AS adapter
RUN apk add --no-cache ca-certificates tzdata wget \
 && adduser -D -H -u 65532 app \
 && mkdir -p /data && chown 65532:65532 /data
WORKDIR /app
COPY --from=build /out/adapter /app/adapter
USER 65532:65532
EXPOSE 8080
ENV HTTP_ADDR=:8080 TZ=Asia/Shanghai
VOLUME ["/data"]
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/adapter"]

FROM alpine:3.21 AS mock-jeepay
RUN apk add --no-cache wget \
 && adduser -D -H -u 65532 app
COPY --from=build-mocks /out/mock-jeepay /app/mock-jeepay
USER 65532:65532
EXPOSE 9216
ENV HTTP_ADDR=:9216
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
  CMD wget -qO- http://127.0.0.1:9216/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/mock-jeepay"]

FROM alpine:3.21 AS mock-newapi
RUN apk add --no-cache wget \
 && adduser -D -H -u 65532 app
COPY --from=build-mocks /out/mock-newapi /app/mock-newapi
USER 65532:65532
EXPOSE 3001
ENV HTTP_ADDR=:3001
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
  CMD wget -qO- http://127.0.0.1:3001/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/mock-newapi"]
