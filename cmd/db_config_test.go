package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDatabaseDSNAddsAStatementTimeout(t *testing.T) {
	cases := []struct {
		name, raw string
		timeout   time.Duration
		want      string
	}{
		{"url", "postgres://u:p@db/neo?sslmode=disable", 30 * time.Second, "postgres://u:p@db/neo?sslmode=disable&statement_timeout=30000"},
		{"url without a query", "postgres://u:p@db/neo", 30 * time.Second, "postgres://u:p@db/neo?statement_timeout=30000"},
		{"url with the operator's own value", "postgres://db/neo?statement_timeout=5000", 30 * time.Second, "postgres://db/neo?statement_timeout=5000"},
		{"key/value", "host=db dbname=neo", 30 * time.Second, "host=db dbname=neo statement_timeout=30000"},
		{"key/value with the operator's own value", "host=db statement_timeout=0", 30 * time.Second, "host=db statement_timeout=0"},
		{"url with a fragment", "postgres://db/neo?sslmode=disable#main", 30 * time.Second, "postgres://db/neo?sslmode=disable&statement_timeout=30000#main"},
		{"url with the operator's value in options", "postgres://db/neo?options=-c%20statement_timeout%3D5000", 30 * time.Second, "postgres://db/neo?options=-c%20statement_timeout%3D5000"},
		{"key/value with spaces around =", "host=db statement_timeout = 0", 30 * time.Second, "host=db statement_timeout = 0"},
		{"key/value with the operator's value in options", "host=db options='-c statement_timeout=5000'", 30 * time.Second, "host=db options='-c statement_timeout=5000'"},
		{"disabled", "postgres://db/neo", 0, "postgres://db/neo"},
	}
	for _, c := range cases {
		if got := databaseDSN(c.raw, c.timeout); got != c.want {
			t.Errorf("%s: databaseDSN(%q) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

func TestConfigurePoolBoundsConnections(t *testing.T) {
	// sql.Open doesn't connect, so no database is needed.
	db, err := sql.Open("postgres", "postgres://localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	configurePool(db, 7)
	if got := db.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("MaxOpenConnections = %d, want 7", got)
	}
}

func TestDatabaseLimitsHaveDefaults(t *testing.T) {
	t.Setenv("DB_MAX_OPEN_CONNS", "")
	t.Setenv("DB_STATEMENT_TIMEOUT_SECONDS", "")
	cfg := loadConfig()
	if cfg.DBMaxOpenConns <= 0 || cfg.DBStatementTimeout <= 0 {
		t.Fatalf("defaults: max open %d, statement timeout %v", cfg.DBMaxOpenConns, cfg.DBStatementTimeout)
	}
	t.Setenv("DB_STATEMENT_TIMEOUT_SECONDS", "0")
	if cfg := loadConfig(); cfg.DBStatementTimeout != 0 {
		t.Fatalf("DB_STATEMENT_TIMEOUT_SECONDS=0 gave %v, want disabled", cfg.DBStatementTimeout)
	}
}

// The timeout reaches the server: a statement running longer is cancelled by
// Postgres. Runs against NEOASSETS_TEST_DATABASE_URL when set (no schema
// changes); skipped otherwise.
func TestStatementTimeoutIsAppliedByTheServer(t *testing.T) {
	url := os.Getenv("NEOASSETS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("NEOASSETS_TEST_DATABASE_URL not set")
	}
	db, err := openDatabase(url, time.Second, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var setting string
	if err := db.QueryRow(`SHOW statement_timeout`).Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if setting != "1s" {
		t.Fatalf("statement_timeout = %q, want 1s", setting)
	}
	_, err = db.Exec(`SELECT pg_sleep(3)`)
	if err == nil || !strings.Contains(err.Error(), "statement timeout") {
		t.Fatalf("a 3 s statement with a 1 s timeout: %v", err)
	}
}
