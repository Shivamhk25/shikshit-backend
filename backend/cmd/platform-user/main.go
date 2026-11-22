// Command platform-user adds a platform (Super Admin) user, or resets one's authenticator after a lost phone
// (decision 0017). Run it on the server; the password is read from standard input so it never appears in the shell
// history or the process list.
//
//	DATABASE_URL=... go run ./cmd/platform-user add -email ops@example.com -name "Asha Verma" < password.txt
//	DATABASE_URL=... go run ./cmd/platform-user reset-mfa -email ops@example.com
//
// A new user sets up their authenticator app at their first sign-in; there is no way to sign in without one.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

func main() {
	if len(os.Args) < 2 || (os.Args[1] != "add" && os.Args[1] != "reset-mfa") {
		fmt.Fprintln(os.Stderr, "usage: platform-user add -email E -name N < password | platform-user reset-mfa -email E")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	email := fs.String("email", "", "email address (used to sign in)")
	name := fs.String("name", "", "display name (add only)")
	_ = fs.Parse(os.Args[2:])

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(2)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	switch os.Args[1] {
	case "add":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(os.Stderr, "read the password from standard input")
			os.Exit(2)
		}
		id, err := identity.CreatePlatformUser(ctx, conn, *email, *name, strings.TrimRight(line, "\r\n"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "add:", err)
			os.Exit(1)
		}
		fmt.Printf("platform user %s added (id %s); they set up their authenticator at first sign-in\n", *email, id)
	case "reset-mfa":
		if err := identity.ResetPlatformMFA(ctx, conn, *email); err != nil {
			fmt.Fprintln(os.Stderr, "reset-mfa:", err)
			os.Exit(1)
		}
		fmt.Printf("authenticator reset for %s; all their sessions are ended\n", *email)
	}
}
