FROM golang:1.21-alpine AS builder

RUN apk add --no-cache \
    git \
    make \
    gcc \
    musl-dev \
    linux-headers

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo \
    -ldflags="-s -w -extldflags '-static'" \
    -o vcitychain main.go

FROM alpine:3.18

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /app/vcitychain /usr/local/bin/vcitychain

# Expose ports for JSON-RPC, gRPC, LibP2P, and Prometheus
EXPOSE 8545 8546 9632 1478 5001

# Create non-root user for security
RUN addgroup -S vcitychain \
    && adduser -S vcitychain -G vcitychain

# Change ownership of the binary
RUN chown vcitychain:vcitychain /usr/local/bin/vcitychain

USER vcitychain

ENTRYPOINT ["vcitychain"]

CMD ["--help"]
