# Stage 1: Build the Go binary
FROM golang:1.25-alpine AS builder

# Set GOTOOLCHAIN to auto to allow downloading newer versions if required by dependencies
ENV GOTOOLCHAIN=auto

# Install build dependencies
RUN apk add --no-cache git

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the application
RUN go build -o main .

# Stage 2: Final runtime image
FROM python:3.11-slim

WORKDIR /app

# Install system dependencies for python libraries if needed
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    python3-dev \
    && rm -rf /var/lib/apt/lists/*

# Copy requirements and install python dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy the binary from builder
COPY --from=builder /app/main .

# Copy other necessary files (scripts, templates, etc.)
COPY extractor.py .

# Expose port
EXPOSE 8080

# Command to run the application
CMD ["./main"]
