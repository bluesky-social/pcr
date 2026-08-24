//go:build integration

package sqlite //nolint:testpackage // query-plan assertions require access to the unexported production query fragments

import (
	"database/sql"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/migrations"

	_ "modernc.org/sqlite"
)

func TestCurrentQueryPlanUsesTagIndexes(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})

	migration, err := fs.ReadFile(migrations.FS, "001_create_change_events.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	where, args := buildCurrentWhereClause(model.CurrentParams{
		ForTeam:    "payments",
		Severities: []string{"sev0", "sev1"},
	})
	// Every interpolated fragment is constructed from production constants;
	// user-provided values remain bound query parameters.
	query := fmt.Sprintf( //nolint:gosec // G201: constant SQL fragments and placeholder-only dynamic predicates
		`EXPLAIN QUERY PLAN %s
		 SELECT event.id %s%s
		 ORDER BY event.timestamp DESC, event.id ASC
		 LIMIT ? OFFSET ?`,
		currentCTEs,
		currentFrom,
		where,
	)
	args = append(args, model.DefaultLimit, 0)
	rows, err := db.QueryContext(t.Context(), query, args...) //nolint:sqlclosecheck // closed by deferred closeQuiet
	if err != nil {
		t.Fatalf("explain current query: %v", err)
	}
	defer closeQuiet(t.Context(), "TestCurrentQueryPlanUsesTagIndexes", rows)

	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
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
	if strings.Contains(strings.ToUpper(plan), "CORRELATED SCALAR SUBQUERY") {
		t.Errorf("query plan contains a correlated subquery:\n%s", plan)
	}
	for _, table := range []string{"SCAN change_events", "SCAN change_event_tags"} {
		if strings.Contains(plan, table) {
			t.Errorf("query plan contains table scan %q:\n%s", table, plan)
		}
	}
}
