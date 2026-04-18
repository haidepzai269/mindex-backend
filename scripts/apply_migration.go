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
	godotenv.Load("../.env")
	dbURL := os.Getenv("DATABASE_URL_CLOUD")
	if dbURL == "" {
		log.Fatal("DATABASE_URL_CLOUD is not set")
	}

	conn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v", err)
	}
	defer conn.Close(context.Background())

	sqlPath := "../migrations/update_persona_v2.sql"
	sqlBytes, err := os.ReadFile(sqlPath)
	if err != nil {
		log.Fatalf("Error reading SQL file: %v", err)
	}

	_, err = conn.Exec(context.Background(), string(sqlBytes))
	if err != nil {
		log.Fatalf("Error executing SQL: %v", err)
	}

	fmt.Println("🎉 Database prompts updated successfully to v2.0!")
}
