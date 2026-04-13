# Mindex Backend

Hệ thống Backend cho dự án Mindex, được xây dựng bằng ngôn ngữ Go.

## Công nghệ sử dụng
- **Go (Golang)**: Core backend.
- **Gin Framework**: Web server & API routing.
- **GORM**: Database ORM.
- **PostgreSQL**: Cơ sở dữ liệu chính (Neon).
- **Redis**: Caching (Upstash).
- **Websockets**: Giao tiếp thời gian thực.

## Cách chạy Local
1. Sao chép `.env.example` thành `.env` và điền các thông số.
2. Chạy `go run main.go`.
