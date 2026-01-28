# MedBeads Core (Go)
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN go build -o medbeads-core main.go

# Runtime
FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/medbeads-core .

# Data Volume Mount Point
RUN mkdir -p /app/medbeads_data

EXPOSE 8080
CMD ["./medbeads-core"]
