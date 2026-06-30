## ── Build stage ──────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /conduit ./cmd/conduit

## ── Runtime stage ─────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12

COPY --from=builder /conduit           /conduit
COPY --from=builder /etc/ssl/certs     /etc/ssl/certs

EXPOSE 8080

ENTRYPOINT ["/conduit", "-config", "/etc/conduit/config.yaml"]
