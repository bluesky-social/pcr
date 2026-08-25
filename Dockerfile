# Build stage
FROM golang:1.27-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/pcr-server ./cmd/server

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

# Create a non-root runtime user.
RUN addgroup -S pcr && adduser -S pcr -G pcr
USER pcr

COPY --from=builder /bin/pcr-server /usr/local/bin/pcr-server

EXPOSE 8080

ENV PCR_ADDR=:8080 \
    PCR_AUTO_MIGRATE=true

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:8080/api/v1/health || exit 1

ENTRYPOINT ["pcr-server"]
