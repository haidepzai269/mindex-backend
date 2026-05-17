package config

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var DB *pgxpool.Pool

func ConnectDB() {
	config, err := pgxpool.ParseConfig(Env.DatabaseURL)
	if err != nil {
		log.Fatalf("Khong the parse cau hinh database: %v", err)
	}

	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET default_transaction_read_only = off")
		return err
	}

	DB, err = pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatalf("Loi ket noi database: %v", err)
	}

	if err := DB.Ping(context.Background()); err != nil {
		log.Fatalf("Khong the ping den database: %v", err)
	}

	log.Println("Connected to PostgreSQL Database")
}

func CloseDB() {
	if DB != nil {
		DB.Close()
		log.Println("Closed PostgreSQL Database connection")
	}
}
