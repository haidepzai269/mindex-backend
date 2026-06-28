# Stage 1: Build the Go binary
FROM golang:1.25-alpine AS builder

ENV GOTOOLCHAIN=auto

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o main .

# Stage 2: Minimal runtime
FROM alpine:3.20

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /app/main .

EXPOSE 8080

CMD ["./main"]
