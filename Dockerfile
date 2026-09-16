# Build stage
FROM golang:1.24-alpine3.21 AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o neoassets ./cmd

# Final stage
FROM alpine:3.21

RUN apk --no-cache add ca-certificates ffmpeg

RUN adduser -D -s /bin/sh assets

WORKDIR /root/

COPY --from=builder /app/neoassets .

COPY --from=builder /app/migrations ./migrations

RUN chown -R assets:assets /root/

USER assets

EXPOSE 8090

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8090/health || exit 1

CMD ["./neoassets"]
