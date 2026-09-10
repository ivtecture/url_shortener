# URL Shortener v2 — Go-приложение. Python-вариант (app/) сохранён.
# CGO_ENABLED=0 + modernc.org/sqlite (pure-Go) -> статический бинарник,
# запускается в финальном образе на голом alpine.
FROM golang:1.27-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/urlshortener .

FROM alpine:3.20

RUN addgroup -S appgroup \
    && adduser -S appuser -G appgroup \
    && mkdir -p /data \
    && chown -R appuser:appgroup /data

COPY --from=builder /out/urlshortener /usr/local/bin/urlshortener

USER appuser

EXPOSE 8000

# busybox-wget из базового образа используется healthcheck'ом compose
CMD ["urlshortener"]