//go:build integration

package postgres_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sarahmaeve/go-prod-change-registry/internal/postgres"
)

func TestMigrateCreatesPostgreSQLSchema(t *testing.T) {
	t.Parallel()
	databaseURL := isolatedDatabaseURL(t)

	if err := postgres.Migrate(databaseURL, time.Second); err != nil {
		t.Fatalf("Migrate(): %v", err)
	}
	if err := postgres.Migrate(databaseURL, time.Second); err != nil {
		t.Fatalf("Migrate() second call: %v", err)
	}

	pool, err := postgres.Open(t.Context(), databaseURL, postgres.PoolOptions{
		MaxConnections: 3,
		ConnectTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(pool.Close)

	var dataType string
	err = pool.QueryRow(t.Context(), `
		SELECT data_type
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'change_events'
		  AND column_name = 'timestamp'
	`).Scan(&dataType)
	if err != nil {
		t.Fatalf("query timestamp column: %v", err)
	}
	if dataType != "timestamp with time zone" {
		t.Errorf("change_events.timestamp data type = %q, want %q", dataType, "timestamp with time zone")
	}

	var isIdentity string
	err = pool.QueryRow(t.Context(), `
		SELECT is_identity
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'change_events'
		  AND column_name = 'ingest_sequence'
	`).Scan(&isIdentity)
	if err != nil {
		t.Fatalf("query ingest_sequence column: %v", err)
	}
	if isIdentity != "YES" {
		t.Errorf("change_events.ingest_sequence is_identity = %q, want YES", isIdentity)
	}
}

func TestMigrateRefusesDirtyDatabase(t *testing.T) {
	t.Parallel()
	databaseURL := isolatedDatabaseURL(t)
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("open dirty migration pool: %v", err)
	}
	t.Cleanup(pool.Close)

	_, err = pool.Exec(t.Context(), `
		CREATE TABLE schema_migrations (
			version BIGINT NOT NULL PRIMARY KEY,
			dirty BOOLEAN NOT NULL
		);
		INSERT INTO schema_migrations (version, dirty) VALUES (1, TRUE);
	`)
	if err != nil {
		t.Fatalf("seed dirty migration: %v", err)
	}

	err = postgres.Migrate(databaseURL, time.Second)
	if err == nil {
		t.Fatal("Migrate() error = nil, want dirty-migration error")
	}
	if !strings.Contains(err.Error(), "dirty at version 1") {
		t.Errorf("Migrate() error = %q, want dirty version context", err)
	}
}

func TestMigrateSerializesConcurrentCallers(t *testing.T) {
	t.Parallel()
	databaseURL := isolatedDatabaseURL(t)

	const callers = 4
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			errs <- postgres.Migrate(databaseURL, time.Second)
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Migrate() concurrent error = %v", err)
		}
	}
}

func isolatedDatabaseURL(t *testing.T) string {
	t.Helper()

	databaseURL := os.Getenv("PCR_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("PCR_TEST_POSTGRES_URL is not set")
	}

	admin, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL test admin pool: %v", err)
	}
	t.Cleanup(admin.Close)

	schema := "pcr_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create test schema %q: %v", schema, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema %q: %v", schema, err)
		}
	})

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse PCR_TEST_POSTGRES_URL: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
