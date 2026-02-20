# Build stage
FROM golang:1.26.0-alpine AS builder

# Version is set at build time (e.g. docker build --build-arg VERSION=v1.0.0)
ARG VERSION=dev

# Install build dependencies
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
# CGO_ENABLED=0 for static binary (no C dependencies)
# -ldflags: -w -s strips debug info; -X stamps version into the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s -X main.version=${VERSION}" -o github-copier .

# Runtime stage - pin to specific version for reproducible builds
FROM alpine:3.21

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

# Create non-root user
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /home/appuser

# Copy binary from builder
COPY --from=builder /app/github-copier .

# Switch to non-root user
USER appuser

# Cloud Run sets PORT environment variable
# Our app reads it from config.Port (defaults to 8080)
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Run the binary
CMD ["./github-copier"]

