// Package dbtest runs tests against a real PostgreSQL 16 (embedded, no Docker needed).
//
// One server is started per test binary on first use; every test gets its own freshly migrated database.
// Tests connect as a non-superuser member of shikshit_app so row-level security really applies.
//
// Usage in a package that needs a database:
//
//	func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }
//	func TestSomething(t *testing.T) { db := dbtest.New(t); ... }
package dbtest

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/migrations"
)

const (
	superUser = "postgres"
	superPass = "postgres"
	appLogin  = "app_login"
	appPass   = "app_login"
)

var (
	mu      sync.Mutex
	server  *embeddedpostgres.EmbeddedPostgres
	port    uint32
	workDir string
	dbSeq   int
)

// Run starts nothing by itself; it runs the tests and then stops the embedded server if one was started.
// Call it from TestMain.
func Run(m *testing.M) int {
	code := m.Run()
	stop()
	return code
}

func stop() {
	mu.Lock()
	defer mu.Unlock()
	if server != nil {
		_ = server.Stop()
		server = nil
	}
	if workDir != "" {
		_ = os.RemoveAll(workDir)
		workDir = ""
	}
}

func start() error {
	mu.Lock()
	defer mu.Unlock()
	if server != nil {
		return nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port = uint32(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()

	workDir, err = os.MkdirTemp("", "shikshit-pg-*")
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(port).
		Username(superUser).Password(superPass).Database("postgres").
		DataPath(filepath.Join(workDir, "data")).
		RuntimePath(filepath.Join(workDir, "runtime")).
		CachePath(filepath.Join(cache, "shikshit-embedded-postgres")).
		StartTimeout(90 * time.Second).
		Logger(io.Discard)
	server = embeddedpostgres.NewDatabase(cfg)
	if err := server.Start(); err != nil {
		server = nil
		return fmt.Errorf("start embedded postgres: %w", err)
	}
	return nil
}

// DB is one freshly migrated database.
type DB struct {
	Name     string
	AdminDSN string // superuser: bypasses row-level security; use it to arrange data
	AppDSN   string // login role in shikshit_app: row-level security applies; use it to act as the API
}

// New creates an empty database, applies every up migration and returns it. The database is dropped when t ends.
func New(t testing.TB) *DB {
	t.Helper()
	d := newEmpty(t)
	ctx := context.Background()
	conn := d.Admin(t)
	defer conn.Close(ctx)
	if err := ApplyUp(ctx, conn, migrations.FS); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN
			CREATE ROLE %s LOGIN PASSWORD '%s' IN ROLE shikshit_app;
		END IF; END $$`, appLogin, appLogin, appPass)); err != nil {
		t.Fatalf("create app login role: %v", err)
	}
	return d
}

// NewEmpty creates a database with no migrations applied (for testing migrations themselves).
func NewEmpty(t testing.TB) *DB {
	t.Helper()
	return newEmpty(t)
}

func newEmpty(t testing.TB) *DB {
	t.Helper()
	if err := start(); err != nil {
		t.Fatalf("%v", err)
	}
	mu.Lock()
	dbSeq++
	name := fmt.Sprintf("t%d_%d", os.Getpid(), dbSeq)
	mu.Unlock()

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn(superUser, superPass, "postgres"))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn(superUser, superPass, "postgres"))
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return &DB{Name: name, AdminDSN: dsn(superUser, superPass, name), AppDSN: dsn(appLogin, appPass, name)}
}

func dsn(user, pass, db string) string {
	return fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", user, pass, port, db)
}

// Admin opens a superuser connection (closed automatically when t ends).
func (d *DB) Admin(t testing.TB) *pgx.Conn { return d.connect(t, d.AdminDSN) }

// App opens a connection as the API role (closed automatically when t ends).
func (d *DB) App(t testing.TB) *pgx.Conn { return d.connect(t, d.AppDSN) }

func (d *DB) connect(t testing.TB, dsn string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// ApplyUp executes every *.up.sql file in lexical order.
func ApplyUp(ctx context.Context, conn *pgx.Conn, fsys fs.FS) error {
	return apply(ctx, conn, fsys, ".up.sql", false)
}

// ApplyDown executes every *.down.sql file in reverse lexical order.
func ApplyDown(ctx context.Context, conn *pgx.Conn, fsys fs.FS) error {
	return apply(ctx, conn, fsys, ".down.sql", true)
}

func apply(ctx context.Context, conn *pgx.Conn, fsys fs.FS, suffix string, reverse bool) error {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), suffix) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if reverse {
		for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
			names[i], names[j] = names[j], names[i]
		}
	}
	for _, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return err
		}
		// No arguments => pgx uses the simple protocol, which allows multi-statement scripts.
		if _, err := conn.Exec(ctx, string(b)); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
	}
	return nil
}
