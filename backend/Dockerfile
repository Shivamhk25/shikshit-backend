# Build stage
FROM golang:1.27.4-alpine3.23 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/server ./cmd/shikshit-api

# Runtime stage
FROM alpine:3.23 AS runtime

RUN apk add --no-cache ca-certificates tzdata curl \
    && addgroup -g 1000 -S appgroup \
    && adduser -u 1000 -S appuser -G appgroup

WORKDIR /app
COPY --from=builder /app/server .

USER appuser
EXPOSE 8080

# Configuration comes from SHIKSHIT_* environment variables (see internal/platform/server/config.go).
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:8080/livez || exit 1

ENTRYPOINT ["./server"]
