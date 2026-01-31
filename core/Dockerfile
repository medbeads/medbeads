# MedBeads Core (Go)
FROM golang:1.23-alpine AS builder

# Install build dependencies for CGO (required by go-sqlite3)
RUN apk add --no-cache gcc musl-dev

WORKDIR /app

# Dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build with CGO enabled
COPY . .
RUN CGO_ENABLED=1 go build -o medbeads-core main.go

# Runtime
FROM alpine:latest

# Install runtime dependencies for SQLite
RUN apk add --no-cache libc6-compat

WORKDIR /app
COPY --from=builder /app/medbeads-core .

# Data Volume Mount Point
RUN mkdir -p /app/medbeads_data

EXPOSE 8080
CMD ["./medbeads-core"]
