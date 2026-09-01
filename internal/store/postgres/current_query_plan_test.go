//go:build integration

package postgres //nolint:testpackage // query-plan assertions exercise unexported production query fragments

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	postgresdb "github.com/sarahmaeve/go-prod-change-registry/internal/postgres"
	"github.com/sarahmaeve/go-prod-change-registry/migrations"
)

func TestCurrentQueryPlanUsesTagIndexes(t *testing.T) {
	t.Parallel()

	pool := queryPlanPool(t)
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("acquire query-plan connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(t.Context(), "SET enable_seqscan = off"); err != nil {
		t.Fatalf("disable sequential scans: %v", err)
	}

	where, args := buildCurrentWhereClause(model.CurrentParams{
		ForTeam:    "payments",
		Severities: []string{"sev0", "sev1"},
	})
	query := fmt.Sprintf(
		`EXPLAIN (FORMAT TEXT) %s
		 SELECT event.id %s%s
		 ORDER BY event.timestamp DESC, event.id ASC
		 LIMIT $%d OFFSET $%d`,
		currentCTEs,
		currentFrom,
		where,
		len(args)+1,
		len(args)+2,
	)
	args = append(args, model.DefaultLimit, 0)
	rows, err := conn.Query(t.Context(), query, args...)
	if err != nil {
		t.Fatalf("explain current query: %v", err)
	}
	defer rows.Close()

	var details []string
	for rows.Next() {
		var detail string
		if err := rows.Scan(&detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}

	plan := strings.Join(details, "\n")
	for _, index := range []string{"idx_change_event_tags_key_value", "idx_change_event_tags_event_key"} {
		if !strings.Contains(plan, index) {
			t.Errorf("query plan does not use %s:\n%s", index, plan)
		}
	}
	if strings.Contains(plan, "Seq Scan on change_event_tags") {
		t.Errorf("query plan performs a tag table scan:\n%s", plan)
	}
}

func queryPlanPool(t *testing.T) *pgxpool.Pool {
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

	schema := "pcr_plan_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create query-plan schema %q: %v", schema, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop query-plan schema %q: %v", schema, err)
		}
	})

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse PCR_TEST_POSTGRES_URL: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()

	pool, err := postgresdb.Open(t.Context(), parsed.String(), postgresdb.PoolOptions{
		MaxConnections: 2,
		ConnectTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open query-plan pool: %v", err)
	}
	t.Cleanup(pool.Close)

	migration, err := fs.ReadFile(migrations.FS, "001_create_change_events.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(t.Context(), string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return pool
}
