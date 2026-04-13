package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func main() {
	err := godotenv.Load(".env")
	if err != nil {
		log.Printf("Error loading .env file: %v", err)
	}

	dbURL := os.Getenv("DATABASE_URL_CLOUD")
	conn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to connect to database: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())

	var count int
	err = conn.QueryRow(context.Background(), "SELECT COUNT(*) FROM token_usage_logs").Scan(&count)
	if err != nil {
		fmt.Printf("Error counting logs: %v\n", err)
	} else {
		fmt.Printf("Total token usage logs: %d\n", count)
	}

	var users int
	err = conn.QueryRow(context.Background(), "SELECT COUNT(*) FROM users").Scan(&users)
	if err != nil {
		fmt.Printf("Error counting users: %v\n", err)
	} else {
		fmt.Printf("Total users: %d\n", users)
	}
}
