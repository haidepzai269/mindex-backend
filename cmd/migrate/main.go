package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load()

	dbURL := os.Getenv("DATABASE_URL_CLOUD")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL_LOCAL")
	}
	if dbURL == "" {
		log.Fatal("DATABASE_URL_CLOUD or DATABASE_URL_LOCAL is required")
	}

	migrationFiles, err := filepath.Glob(filepath.Join("migrations", "*.sql"))
	if err != nil {
		log.Fatalf("Failed to list migration files: %v", err)
	}
	sort.Strings(migrationFiles)
	if len(migrationFiles) == 0 {
		log.Fatal("No migration files found in backend/migrations")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		log.Fatalf("Failed to connect DB: %v", err)
	}
	defer conn.Close(ctx)

	success := 0
	for _, path := range migrationFiles {
		sqlBytes, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("Failed to read %s: %v", path, err)
		}

		sql := strings.TrimSpace(string(sqlBytes))
		if sql == "" {
			fmt.Printf("  -  [%-40s] skipped empty file\n", filepath.Base(path))
			continue
		}

		if _, err := conn.Exec(ctx, sql); err != nil {
			log.Fatalf("Migration %s failed: %v", filepath.Base(path), err)
		}

		fmt.Printf("  ok [%-40s]\n", filepath.Base(path))
		success++
	}

	fmt.Printf("\nMigrations completed: %d/%d applied\n", success, len(migrationFiles))
}
