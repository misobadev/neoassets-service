// Package testdb gives tests a migrated PostgreSQL database.
//
// Tests that need a database call Open with a name unique to their package.
// The database named by NEOASSETS_TEST_DATABASE_URL is only used to create a
// sibling database "<base>_<name>", which is reset and migrated once per test
// process. Packages therefore never share a schema, so `go test ./...` can run
// them in parallel. Without the variable the calling test is skipped.
package testdb

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/lib/pq"
)

// EnvVar names the base test database. Its name must contain "test".
const EnvVar = "NEOASSETS_TEST_DATABASE_URL"

var (
	mu     sync.Mutex
	opened = map[string]*result{}
	nameRe = regexp.MustCompile(`^[a-z0-9_]+$`)
)

type result struct {
	db  *sql.DB
	err error
}

// Open returns a freshly migrated database dedicated to name (for example the
// package name). The first call in a test process drops and re-creates its
// public schema; later calls with the same name reuse it.
func Open(t testing.TB, name string) *sql.DB {
	t.Helper()
	base := lookupEnv()
	if base == "" {
		t.Skip(EnvVar + " not set")
	}
	if !nameRe.MatchString(name) {
		t.Fatalf("testdb: invalid name %q", name)
	}
	mu.Lock()
	defer mu.Unlock()
	r, ok := opened[name]
	if !ok {
		db, err := open(base, name)
		r = &result{db: db, err: err}
		opened[name] = r
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	return r.db
}

func open(base, name string) (*sql.DB, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("testdb: parse %s: %w", EnvVar, err)
	}
	baseName := strings.TrimPrefix(u.Path, "/")
	if !strings.Contains(strings.ToLower(baseName), "test") {
		return nil, fmt.Errorf("testdb: refusing to use database %q: its name must contain \"test\"", baseName)
	}
	dbName := baseName + "_" + name

	admin, err := sql.Open("postgres", base)
	if err != nil {
		return nil, err
	}
	defer admin.Close()
	var exists bool
	if err := admin.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName).Scan(&exists); err != nil {
		return nil, fmt.Errorf("testdb: %w", err)
	}
	if !exists {
		if _, err := admin.Exec(`CREATE DATABASE ` + pq.QuoteIdentifier(dbName)); err != nil {
			return nil, fmt.Errorf("testdb: create %s: %w", dbName, err)
		}
	}

	u.Path = "/" + dbName
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		db.Close()
		return nil, fmt.Errorf("testdb: reset %s: %w", dbName, err)
	}
	if err := migrateUp(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrateUp(db *sql.DB) error {
	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("testdb: migrate driver: %w", err)
	}
	m, err := migrate.NewWithDatabaseInstance("file://"+MigrationsDir(), "postgres", driver)
	if err != nil {
		return fmt.Errorf("testdb: migrate: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("testdb: migrate up: %w", err)
	}
	return nil
}

// MigrationsDir is the absolute path of the repository's migrations folder.
func MigrationsDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

// lookupEnv is a variable so the package's own tests can stub it.
var lookupEnv = func() string { return strings.TrimSpace(os.Getenv(EnvVar)) }
