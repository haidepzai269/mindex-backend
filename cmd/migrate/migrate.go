package main

import (
	"context"
	"log"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func executeSQLFile(conn *pgx.Conn, filepath string) {
	sqlBytes, err := os.ReadFile(filepath)
	if err != nil {
		log.Fatalf("Error reading %s: %v\n", filepath, err)
	}

	_, err = conn.Exec(context.Background(), string(sqlBytes))
	if err != nil {
		log.Fatalf("Error executing %s: %v\n", filepath, err)
	}
	log.Printf("✅ Executed %s successfully.\n", filepath)
}

func main() {
	err := godotenv.Load(".env")
	if err != nil {
		log.Println("No .env file found via godotenv, using system env")
	}

	dbURL := os.Getenv("DATABASE_URL_CLOUD")
	if dbURL == "" {
		log.Fatal("DATABASE_URL_CLOUD is not set in .env")
	}

	log.Printf("Connecting to NeonDB: %s...\n", dbURL)
	conn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer conn.Close(context.Background())

	log.Println("Connection established! Running SQL files...")

	// 1. Initial schema
	// executeSQLFile(conn, "d:\\Mindex\\system.sql")

	// 2. Admin migration
	executeSQLFile(conn, "d:\\Mindex\\admin_migration.sql")

	// 3. Multi persona supplement
	executeSQLFile(conn, "d:\\Mindex\\multi_persona.sql")

	log.Println("🎉 All migrations ran successfully on Neon DB!")
}
