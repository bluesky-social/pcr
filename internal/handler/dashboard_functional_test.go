//go:build integration

package handler_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/config"
	"github.com/sarah/go-prod-change-registry/internal/fixture"
	"github.com/sarah/go-prod-change-registry/internal/handler"
	"github.com/sarah/go-prod-change-registry/internal/middleware"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/router"
	"github.com/sarah/go-prod-change-registry/internal/service"
	"github.com/sarah/go-prod-change-registry/internal/store/sqlite"
	"github.com/sarah/go-prod-change-registry/migrations"

	_ "modernc.org/sqlite"
)

func TestSeededDashboardViews(t *testing.T) {
	t.Parallel()

	r := seededDashboardRouter(t)

	current := renderDashboard(t, r, "/?view=current&team=payments")
	for _, want := range []string{
		`data-interface="phosphor-deck"`,
		`class="dashboard-summary instrument-housing"`,
		"Active high-severity changes",
		"Ledger reconciliation backfill",
		"Primary card vault key rotation",
		"West coast edge traffic evacuation",
		"Orders archive compaction",
	} {
		if !strings.Contains(current, want) {
			t.Errorf("Current response does not contain %q", want)
		}
	}
	for _, unwanted := range []string{
		"Production signal deck",
		"Change operations",
		"Immutable production telemetry",
		"Registry online",
		"SYS/01",
		"Live telemetry",
	} {
		if strings.Contains(current, unwanted) {
			t.Errorf("Current response contains decorative copy %q", unwanted)
		}
	}
	currentTable := dashboardTableBody(t, current)
	for _, excluded := range []string{
		"Checkout API 2026.08.24.3 rollout",
		"Ingress controller fleet replacement",
		"Primary card vault key rotation (redelivery)",
		"Legacy event without a logical operation ID",
	} {
		if strings.Contains(currentTable, excluded) {
			t.Errorf("Current table unexpectedly contains %q", excluded)
		}
	}

	apiRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/current?for_team=payments", nil)
	apiResponse := httptest.NewRecorder()
	r.ServeHTTP(apiResponse, apiRequest)
	var currentResult model.ListResult
	if err := json.NewDecoder(apiResponse.Body).Decode(&currentResult); err != nil {
		t.Fatalf("decode seeded Current API response: %v", err)
	}
	if apiResponse.Code != http.StatusOK || currentResult.TotalCount != 4 {
		t.Errorf("seeded Current API = status %d, total %d; want 200, 4", apiResponse.Code, currentResult.TotalCount)
	}

	site := dashboardTableBody(t, renderDashboard(t, r, "/?view=site"))
	if !strings.Contains(site, "West coast edge traffic evacuation") {
		t.Error("Site-wide table does not contain the site incident")
	}
	for _, excluded := range []string{"Ledger reconciliation backfill", "Orders archive compaction", "Ingress controller fleet replacement"} {
		if strings.Contains(site, excluded) {
			t.Errorf("Site-wide table unexpectedly contains %q", excluded)
		}
	}

	history := dashboardTableBody(t, renderDashboard(t, r, "/"))
	for _, want := range []string{
		"Checkout risk model advanced to cohort 40%",
		"2 links",
		"Checkout API 2026.08.24.3 rollout",
		"Checkout API 2026.08.24.3 rollout completed",
		"Legacy event without a logical operation ID",
	} {
		if !strings.Contains(history, want) {
			t.Errorf("History table does not contain %q", want)
		}
	}
	if strings.Contains(history, "Ledger reconciliation backfill") {
		t.Error("default 24-hour History contains the two-week-old operation")
	}

	listRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?external_id=demo-history-release", nil)
	listResponse := httptest.NewRecorder()
	r.ServeHTTP(listResponse, listRequest)
	var historyResult model.ListResult
	if err := json.NewDecoder(listResponse.Body).Decode(&historyResult); err != nil {
		t.Fatalf("decode event list for detail link check: %v", err)
	}
	var linkedEventID string
	for _, event := range historyResult.Events {
		if event.ExternalID == "demo-history-release" {
			linkedEventID = event.ID
			break
		}
	}
	if linkedEventID == "" {
		t.Fatal("fixture event with links not found")
	}
	detail := renderDashboard(t, r, "/events/"+linkedEventID)
	for _, want := range []string{
		`href="https://github.com/example/checkout/pull/482"`,
		`>Rollout PR</a>`,
		`href="https://example.pagerduty.com/incidents/PDEMO"`,
		`>PagerDuty incident</a>`,
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("Detail response does not contain link markup %q", want)
		}
	}

	alerts := dashboardTableBody(t, renderDashboard(t, r, "/?view=alerts"))
	if !strings.Contains(alerts, "Checkout risk model advanced to cohort 40%") {
		t.Error("Alerts table does not contain the actively alerted history event")
	}

	fontRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/static/fonts/Orbitron-Variable.ttf", nil)
	fontResponse := httptest.NewRecorder()
	r.ServeHTTP(fontResponse, fontRequest)
	if fontResponse.Code != http.StatusOK || fontResponse.Body.Len() < 1000 {
		t.Errorf("vendored font response = status %d, bytes %d", fontResponse.Code, fontResponse.Body.Len())
	}

	cssRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/static/style.css", nil)
	cssResponse := httptest.NewRecorder()
	r.ServeHTTP(cssResponse, cssRequest)
	if cssResponse.Code != http.StatusOK || !strings.Contains(cssResponse.Body.String(), "/static/fonts/ChakraPetch-Regular.ttf") {
		t.Errorf("stylesheet does not reference the local body font; status = %d", cssResponse.Code)
	}
}

func seededDashboardRouter(t *testing.T) http.Handler {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "dashboard.db")
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(on)&_txlock=immediate", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	for _, name := range []string{"001_create_change_events.up.sql", "002_create_change_event_links.up.sql"} {
		migration, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.ExecContext(t.Context(), string(migration)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}

	store := sqlite.New(db, time.Second)
	svc := service.NewChangeService(store)
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve functional test path")
	}
	fixturePath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", "functional", "phosphor-demo.json")
	file, err := os.Open(fixturePath) //nolint:gosec // G304: path is rooted at this test source file
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = file.Close() }()
	events, err := fixture.Load(file)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if _, err := fixture.Apply(t.Context(), svc, events); err != nil {
		t.Fatalf("apply fixture: %v", err)
	}

	apiHandler := handler.NewAPIHandler(svc, db)
	dashboardHandler := handler.NewDashboardHandler(svc, 0, []byte("functional-test-session-secret-32b"))
	loginHandler := handler.NewLoginHandler([]string{"demo-token"}, middleware.SessionOptions{Secret: []byte("functional-test-session-secret-32b")})
	return router.New(apiHandler, dashboardHandler, loginHandler, &config.Config{
		APITokens:        []string{"demo-token"},
		RequireAuthReads: false,
		SessionSecret:    []byte("functional-test-session-secret-32b"),
	})
}

func renderDashboard(t *testing.T, handler http.Handler, path string) string {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d; body = %s", path, response.Code, response.Body.String())
	}
	return response.Body.String()
}

func dashboardTableBody(t *testing.T, page string) string {
	t.Helper()
	_, after, ok := strings.Cut(page, "<tbody>")
	if !ok {
		t.Fatal("dashboard response has no table body")
	}
	body, _, ok := strings.Cut(after, "</tbody>")
	if !ok {
		t.Fatal("dashboard response has no closing table body")
	}
	return body
}
