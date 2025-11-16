# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

WORKDIR /workspace

# Copy go mod files
COPY go.mod go.mod
COPY go.sum go.sum

# Download dependencies
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager cmd/main.go

# Runtime stage
FROM alpine:3.18

# Install required tools for backup operations
RUN apk add --no-cache \
    ca-certificates \
    tar \
    gzip \
    aws-cli \
    bash

WORKDIR /

# Copy the binary from builder
COPY --from=builder /workspace/manager .

# Create user
RUN adduser -D -u 65532 bardiup
USER 65532:65532

ENTRYPOINT ["/manager"]
