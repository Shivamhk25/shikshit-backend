// Command seed creates the demo institute (idempotent). It prints counts only, never credentials.
//
//	DATABASE_URL=postgres://owner:pass@localhost:5432/db?sslmode=disable go run ./cmd/seed
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/seed"
)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required (a connection that owns the schema; run `make migrate-up` first)")
		os.Exit(2)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	res, err := seed.Demo(ctx, conn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	fmt.Printf("demo institute %q ready (id %s): %d module(s) added, %d role(s) created\n", seed.DemoCode, res.TenantID, res.ModulesAdded, res.RolesCreated)
}
