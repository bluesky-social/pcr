//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sarah/go-prod-change-registry/internal/model"
	postgresdb "github.com/sarah/go-prod-change-registry/internal/postgres"
	"github.com/sarah/go-prod-change-registry/internal/store"
	"github.com/sarah/go-prod-change-registry/internal/store/postgres"
	"github.com/sarah/go-prod-change-registry/migrations"
)

// ---------------------------------------------------------------------------
// Test infrastructure
// ---------------------------------------------------------------------------

// applyTestMigrations reads and executes the embedded migration SQL files in order.
func applyTestMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	sqlBytes, err := fs.ReadFile(migrations.FS, "001_create_change_events.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(t.Context(), string(sqlBytes)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
}

func newTestStore(t *testing.T) *postgres.Store {
	t.Helper()
	return postgres.New(newTestPool(t), 100*time.Millisecond)
}

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := isolatedDatabaseURL(t)
	pool, err := postgresdb.Open(t.Context(), databaseURL, postgresdb.PoolOptions{
		MaxConnections: 10,
		ConnectTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open PostgreSQL test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyTestMigrations(t, pool)
	return pool
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

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("mustTime: %v", err)
	}
	return ts
}

func makeEvent(id, userName, eventType string, ts time.Time, tags map[string]string) *model.ChangeEvent {
	return &model.ChangeEvent{
		ID:              id,
		UserName:        userName,
		Timestamp:       ts,
		EventType:       eventType,
		Description:     "desc-" + id,
		LongDescription: "long-desc-" + id,
		Tags:            tags,
		CreatedAt:       ts,
	}
}

// ---------------------------------------------------------------------------
// Create tests
// ---------------------------------------------------------------------------

func TestCreate(t *testing.T) {
	t.Parallel()

	t.Run("event with all fields", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-01-15T10:00:00Z")
		ev := &model.ChangeEvent{
			ID:              "evt-001",
			UserName:        "alice",
			UserProvider:    "github",
			UserSubject:     "12345",
			Timestamp:       ts,
			EventType:       model.EventTypeDeployment,
			Description:     "deploy v1.2.3",
			LongDescription: "Rolling deploy of service-foo to v1.2.3",
			Links: []model.EventLink{
				{Label: "PagerDuty incident", URL: "https://example.pagerduty.com/incidents/PABC"},
				{Label: "Rollout PR", URL: "https://github.com/example/service-foo/pull/123"},
			},
			Tags:      map[string]string{"env": "prod", "service": "foo", "region": "us-east-1"},
			CreatedAt: ts,
		}

		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		if got.ID != ev.ID {
			t.Errorf("ID = %q, want %q", got.ID, ev.ID)
		}
		if got.UserName != ev.UserName {
			t.Errorf("UserName = %q, want %q", got.UserName, ev.UserName)
		}
		if got.UserProvider != ev.UserProvider || got.UserSubject != ev.UserSubject {
			t.Errorf("identity = %q/%q, want %q/%q", got.UserProvider, got.UserSubject, ev.UserProvider, ev.UserSubject)
		}
		if !got.Timestamp.Equal(ev.Timestamp) {
			t.Errorf("Timestamp = %v, want %v", got.Timestamp, ev.Timestamp)
		}
		if got.EventType != ev.EventType {
			t.Errorf("EventType = %q, want %q", got.EventType, ev.EventType)
		}
		if got.Description != ev.Description {
			t.Errorf("Description = %q, want %q", got.Description, ev.Description)
		}
		if got.LongDescription != ev.LongDescription {
			t.Errorf("LongDescription = %q, want %q", got.LongDescription, ev.LongDescription)
		}
		if got.ParentID != "" {
			t.Errorf("ParentID = %q, want empty", got.ParentID)
		}
		if len(got.Links) != 2 || got.Links[0].Label != "PagerDuty incident" || got.Links[1].Label != "Rollout PR" {
			t.Errorf("created Links = %#v, want both links in order", got.Links)
		}
		if len(got.Tags) != 3 {
			t.Fatalf("len(Tags) = %d, want 3", len(got.Tags))
		}
		for k, v := range ev.Tags {
			if got.Tags[k] != v {
				t.Errorf("Tags[%q] = %q, want %q", k, got.Tags[k], v)
			}
		}

		stored, err := s.GetByID(ctx, ev.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if stored.UserProvider != ev.UserProvider || stored.UserSubject != ev.UserSubject {
			t.Errorf("stored identity = %q/%q, want %q/%q", stored.UserProvider, stored.UserSubject, ev.UserProvider, ev.UserSubject)
		}
		if len(stored.Links) != 2 || stored.Links[0].URL != ev.Links[0].URL || stored.Links[1].URL != ev.Links[1].URL {
			t.Errorf("stored Links = %#v, want %#v", stored.Links, ev.Links)
		}
	})

	t.Run("event with tags", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-02-01T09:00:00Z")
		ev := makeEvent("evt-tags", "bob", model.EventTypeFeatureFlag, ts, map[string]string{"flag": "dark-mode", "team": "frontend"})

		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if len(got.Tags) != 2 {
			t.Fatalf("len(Tags) = %d, want 2", len(got.Tags))
		}
		if got.Tags["flag"] != "dark-mode" {
			t.Errorf("Tags[flag] = %q, want %q", got.Tags["flag"], "dark-mode")
		}
		if got.Tags["team"] != "frontend" {
			t.Errorf("Tags[team] = %q, want %q", got.Tags["team"], "frontend")
		}
	})

	t.Run("SQL-looking form values remain data", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ts := mustTime(t, "2026-08-26T12:00:00Z")
		payload := `value'); DROP TABLE change_events; --`
		event := &model.ChangeEvent{
			ID:              "sql-looking-event",
			ExternalID:      payload,
			UserName:        payload,
			Timestamp:       ts,
			EventType:       payload,
			Description:     payload,
			LongDescription: payload,
			Links:           []model.EventLink{{Label: payload, URL: "https://example.com/?q=%27%3Bdrop"}},
			Tags:            map[string]string{payload: payload},
			CreatedAt:       ts,
		}
		if _, err := s.Create(t.Context(), event); err != nil {
			t.Fatalf("Create(SQL-looking values): %v", err)
		}

		stored, err := s.GetByID(t.Context(), event.ID)
		if err != nil {
			t.Fatalf("GetByID(SQL-looking values): %v", err)
		}
		if stored == nil {
			t.Fatal("GetByID(SQL-looking values) = nil, want event")
		}
		if len(stored.Links) != 1 {
			t.Fatalf("len(stored.Links) = %d, want 1", len(stored.Links))
		}
		if stored.Description != payload || stored.EventType != payload || stored.Tags[payload] != payload || stored.Links[0].Label != payload {
			t.Errorf("stored SQL-looking values = %#v, want values preserved as data", stored)
		}

		probe := makeEvent("after-sql-looking-event", "alice", model.EventTypeDeployment, ts.Add(time.Minute), nil)
		if _, err := s.Create(t.Context(), probe); err != nil {
			t.Fatalf("Create() after SQL-looking values: %v", err)
		}
	})

	t.Run("meta-event with parent_id", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-01T10:00:00Z")
		parent := makeEvent("parent-1", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		meta := &model.ChangeEvent{
			ID:        "star-1",
			ParentID:  "parent-1",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		got, err := s.Create(ctx, meta)
		if err != nil {
			t.Fatalf("Create meta: %v", err)
		}
		if got.ParentID != "parent-1" {
			t.Errorf("ParentID = %q, want %q", got.ParentID, "parent-1")
		}
		if got.EventType != model.EventTypeStar {
			t.Errorf("EventType = %q, want %q", got.EventType, model.EventTypeStar)
		}
		if !got.IsMetaEvent() {
			t.Error("IsMetaEvent() = false, want true")
		}
	})

	t.Run("minimal fields no tags", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-02-15T09:00:00Z")
		ev := &model.ChangeEvent{
			ID:        "evt-min",
			UserName:  "carol",
			Timestamp: ts,
			EventType: model.EventTypeK8sChange,
			CreatedAt: ts,
		}
		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got.LongDescription != "" {
			t.Errorf("LongDescription = %q, want empty", got.LongDescription)
		}
		// Tags can be nil or empty when none set.
		if len(got.Tags) != 0 {
			t.Errorf("len(Tags) = %d, want 0", len(got.Tags))
		}
	})
}

func TestChangeEventTagKeysAreUnique(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t)

	_, err := pool.Exec(
		t.Context(),
		`INSERT INTO change_events
		 (id, user_name, timestamp, event_type, description, long_description, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		"event-with-tag",
		"alice",
		mustTime(t, "2026-08-24T12:00:00Z"),
		model.EventTypeDeployment,
		"test event",
		"",
		mustTime(t, "2026-08-24T12:00:00Z"),
	)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := pool.Exec(
		t.Context(),
		`INSERT INTO change_event_tags (event_id, key, value) VALUES ($1, $2, $3)`,
		"event-with-tag",
		"team",
		"payments",
	); err != nil {
		t.Fatalf("insert first tag: %v", err)
	}

	_, err = pool.Exec(
		t.Context(),
		`INSERT INTO change_event_tags (event_id, key, value) VALUES ($1, $2, $3)`,
		"event-with-tag",
		"team",
		"platform",
	)
	if err == nil {
		t.Fatal("inserting a second value for one event tag key succeeded")
	}
}

func TestCreateReturnsCanonicalDefensiveCopy(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	offset := time.FixedZone("test-offset", 2*60*60)
	timestamp := time.Date(2026, 8, 24, 12, 34, 56, 789_000_000, offset)
	createdAt := timestamp.Add(time.Second)
	event := makeEvent("canonical-copy", "alice", model.EventTypeDeployment, timestamp, map[string]string{"env": "prod"})
	event.CreatedAt = createdAt

	got, err := s.Create(t.Context(), event)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got == event {
		t.Fatal("Create() returned the caller-owned event pointer")
	}
	if want := timestamp.UTC().Truncate(time.Second); !got.Timestamp.Equal(want) || got.Timestamp.Location() != time.UTC {
		t.Errorf("Timestamp = %v (%v), want %v (UTC)", got.Timestamp, got.Timestamp.Location(), want)
	}
	if want := createdAt.UTC().Truncate(time.Second); !got.CreatedAt.Equal(want) || got.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt = %v (%v), want %v (UTC)", got.CreatedAt, got.CreatedAt.Location(), want)
	}

	event.Tags["env"] = "caller-mutated"
	if got.Tags["env"] != "prod" {
		t.Errorf("returned Tags changed with input map: got %q, want prod", got.Tags["env"])
	}
	got.Tags["env"] = "result-mutated"
	persisted, err := s.GetByID(t.Context(), event.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if persisted.Tags["env"] != "prod" {
		t.Errorf("persisted Tags = %q after result mutation, want prod", persisted.Tags["env"])
	}
}

func TestToggleStarIsAtomic(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	if _, err := s.ToggleStar(t.Context(), "missing", model.UserIdentity{Name: "alice"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ToggleStar(missing) error = %v, want %v", err, store.ErrNotFound)
	}

	parent := makeEvent("toggle-parent", "alice", model.EventTypeDeployment, time.Now().UTC(), nil)
	if _, err := s.Create(t.Context(), parent); err != nil {
		t.Fatalf("Create(parent) error = %v", err)
	}

	const toggles = 20
	start := make(chan struct{})
	errs := make(chan error, toggles)
	var wg sync.WaitGroup
	for range toggles {
		wg.Go(func() {
			<-start
			_, err := s.ToggleStar(t.Context(), parent.ID, model.UserIdentity{Name: "concurrent-user"})
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("ToggleStar() error = %v", err)
		}
	}

	annotations, err := s.GetAnnotations(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("GetAnnotations() error = %v", err)
	}
	if annotations.Starred {
		t.Fatal("even number of concurrent toggles left event starred")
	}

	if _, err := s.ToggleStar(t.Context(), parent.ID, model.UserIdentity{Name: "concurrent-user"}); err != nil {
		t.Fatalf("final ToggleStar() error = %v", err)
	}
	annotations, err = s.GetAnnotations(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("GetAnnotations() after final toggle error = %v", err)
	}
	if !annotations.Starred {
		t.Fatal("odd number of toggles left event unstarred")
	}
}

func TestToggleStarIsAtomicAcrossPools(t *testing.T) {
	t.Parallel()

	databaseURL := isolatedDatabaseURL(t)
	openPool := func() *pgxpool.Pool {
		pool, err := postgresdb.Open(t.Context(), databaseURL, postgresdb.PoolOptions{
			MaxConnections: 10,
			ConnectTimeout: time.Second,
		})
		if err != nil {
			t.Fatalf("open PostgreSQL test pool: %v", err)
		}
		t.Cleanup(pool.Close)
		return pool
	}

	firstPool := openPool()
	secondPool := openPool()
	applyTestMigrations(t, firstPool)
	stores := []*postgres.Store{
		postgres.New(firstPool, time.Second),
		postgres.New(secondPool, time.Second),
	}

	parent := makeEvent("cross-pool-toggle-parent", "alice", model.EventTypeDeployment, time.Now().UTC(), nil)
	if _, err := stores[0].Create(t.Context(), parent); err != nil {
		t.Fatalf("Create(parent) error = %v", err)
	}

	const toggles = 20
	ctx := t.Context()
	start := make(chan struct{})
	errs := make(chan error, toggles)
	var wg sync.WaitGroup
	for i := range toggles {
		store := stores[i%len(stores)]
		wg.Go(func() {
			<-start
			_, err := store.ToggleStar(ctx, parent.ID, model.UserIdentity{Name: "concurrent-user"})
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("ToggleStar() error = %v", err)
		}
	}

	annotations, err := stores[1].GetAnnotations(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("GetAnnotations() error = %v", err)
	}
	if annotations.Starred {
		t.Fatal("even number of cross-pool toggles left event starred")
	}
}

func TestToggleAlertAppendsOppositeTransitions(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	parent := makeEvent("toggle-alert-parent", "alice", model.EventTypeDeployment, time.Now().UTC(), nil)
	if _, err := s.Create(t.Context(), parent); err != nil {
		t.Fatalf("Create(parent) error = %v", err)
	}

	opened, err := s.ToggleAlert(t.Context(), parent.ID, model.UserIdentity{Name: "on-call"})
	if err != nil {
		t.Fatalf("first ToggleAlert() error = %v", err)
	}
	if opened.EventType != model.EventTypeAlert || opened.ParentID != parent.ID {
		t.Errorf("first transition = %+v", opened)
	}
	cleared, err := s.ToggleAlert(t.Context(), parent.ID, model.UserIdentity{Name: "on-call"})
	if err != nil {
		t.Fatalf("second ToggleAlert() error = %v", err)
	}
	if cleared.EventType != model.EventTypeClearAlert {
		t.Errorf("second transition type = %q, want %q", cleared.EventType, model.EventTypeClearAlert)
	}
	annotations, err := s.GetAnnotations(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("GetAnnotations() error = %v", err)
	}
	if annotations.Alerted {
		t.Fatal("two alert toggles left event alerted")
	}
}

// ---------------------------------------------------------------------------
// ExternalID / idempotency tests
// ---------------------------------------------------------------------------

func TestCreateExternalID(t *testing.T) {
	t.Parallel()

	t.Run("duplicate external_id returns existing event", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ev := makeEvent("ext-1", "alice", model.EventTypeDeployment, time.Now().UTC(), map[string]string{"env": "prod"})
		ev.ExternalID = "gh-actions-run-123"

		created, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		// Create again with same external_id but different ID.
		ev2 := makeEvent("ext-2", "bob", model.EventTypeDeployment, time.Now().UTC(), nil)
		ev2.ExternalID = "gh-actions-run-123"

		duplicate, err := s.Create(ctx, ev2)
		if !errors.Is(err, store.ErrDuplicate) {
			t.Fatalf("expected store.ErrDuplicate, got %v", err)
		}
		if duplicate == nil {
			t.Fatal("expected existing event to be returned alongside ErrDuplicate")
		}
		if duplicate.ID != created.ID {
			t.Errorf("duplicate.ID = %q, want %q (original)", duplicate.ID, created.ID)
		}
		if duplicate.ExternalID != "gh-actions-run-123" {
			t.Errorf("duplicate.ExternalID = %q, want %q", duplicate.ExternalID, "gh-actions-run-123")
		}
	})

	t.Run("different external_ids create separate events", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ev1 := makeEvent("diff-ext-1", "alice", model.EventTypeDeployment, time.Now().UTC(), nil)
		ev1.ExternalID = "run-aaa"

		ev2 := makeEvent("diff-ext-2", "bob", model.EventTypeDeployment, time.Now().UTC(), nil)
		ev2.ExternalID = "run-bbb"

		created1, err := s.Create(ctx, ev1)
		if err != nil {
			t.Fatalf("Create ev1: %v", err)
		}
		created2, err := s.Create(ctx, ev2)
		if err != nil {
			t.Fatalf("Create ev2: %v", err)
		}

		if created1.ID == created2.ID {
			t.Errorf("expected different IDs, both got %q", created1.ID)
		}
	})

	t.Run("empty external_id does not conflict", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ev1 := makeEvent("empty-ext-1", "alice", model.EventTypeDeployment, time.Now().UTC(), nil)
		// ExternalID left empty (zero value).

		ev2 := makeEvent("empty-ext-2", "bob", model.EventTypeDeployment, time.Now().UTC(), nil)
		// ExternalID left empty (zero value).

		_, err := s.Create(ctx, ev1)
		if err != nil {
			t.Fatalf("Create ev1: %v", err)
		}
		_, err = s.Create(ctx, ev2)
		if err != nil {
			t.Fatalf("Create ev2: %v (empty external_id should not conflict)", err)
		}
	})

	t.Run("external_id is returned in GetByID", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ev := makeEvent("ext-get-1", "carol", model.EventTypeK8sChange, time.Now().UTC(), nil)
		ev.ExternalID = "jenkins-build-42"

		_, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := s.GetByID(ctx, "ext-get-1")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got == nil {
			t.Fatal("GetByID returned nil")
		}
		if got.ExternalID != "jenkins-build-42" {
			t.Errorf("ExternalID = %q, want %q", got.ExternalID, "jenkins-build-42")
		}
	})
}

// ---------------------------------------------------------------------------
// GetByID tests
// ---------------------------------------------------------------------------

func TestGetByID(t *testing.T) {
	t.Parallel()

	t.Run("existing event", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ev := makeEvent("get-1", "carol", model.EventTypeK8sChange, mustTime(t, "2026-03-01T08:00:00Z"), map[string]string{"cluster": "prod-1"})
		if _, err := s.Create(ctx, ev); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := s.GetByID(ctx, "get-1")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got == nil {
			t.Fatal("GetByID returned nil")
		}
		if got.ID != "get-1" {
			t.Errorf("ID = %q, want %q", got.ID, "get-1")
		}
		if got.UserName != "carol" {
			t.Errorf("UserName = %q, want %q", got.UserName, "carol")
		}
		if got.Tags["cluster"] != "prod-1" {
			t.Errorf("Tags[cluster] = %q, want %q", got.Tags["cluster"], "prod-1")
		}
	})

	t.Run("non-existent returns nil", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		got, err := s.GetByID(ctx, "does-not-exist")
		if err != nil {
			t.Fatalf("GetByID error: %v", err)
		}
		if got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("event with parent_id", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-05T10:00:00Z")
		parent := makeEvent("parent-get", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		meta := &model.ChangeEvent{
			ID:        "meta-get",
			ParentID:  "parent-get",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, meta); err != nil {
			t.Fatalf("Create meta: %v", err)
		}

		got, err := s.GetByID(ctx, "meta-get")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got == nil {
			t.Fatal("GetByID returned nil")
		}
		if got.ParentID != "parent-get" {
			t.Errorf("ParentID = %q, want %q", got.ParentID, "parent-get")
		}
	})
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

// seedEvents inserts a known set of events for list tests and returns them.
func seedEvents(t *testing.T, s *postgres.Store) []model.ChangeEvent {
	t.Helper()
	ctx := context.Background()

	events := []*model.ChangeEvent{
		makeEvent("list-1", "alice", model.EventTypeDeployment, mustTime(t, "2026-01-01T10:00:00Z"), map[string]string{"env": "prod", "service": "api"}),
		makeEvent("list-2", "bob", model.EventTypeFeatureFlag, mustTime(t, "2026-01-02T10:00:00Z"), map[string]string{"env": "prod"}),
		makeEvent("list-3", "alice", model.EventTypeK8sChange, mustTime(t, "2026-01-03T10:00:00Z"), map[string]string{"env": "staging", "cluster": "us-west"}),
		makeEvent("list-4", "carol", model.EventTypeDeployment, mustTime(t, "2026-01-04T10:00:00Z"), map[string]string{"env": "prod", "service": "web"}),
		makeEvent("list-5", "alice", model.EventTypeDeployment, mustTime(t, "2026-01-05T10:00:00Z"), nil),
	}

	var created []model.ChangeEvent
	for _, ev := range events {
		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("seed Create(%s): %v", ev.ID, err)
		}
		created = append(created, *got)
	}
	return created
}

func TestList(t *testing.T) {
	t.Parallel()

	t.Run("no filters returns all", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		seedEvents(t, s)
		ctx := context.Background()

		res, err := s.List(ctx, model.ListParams{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if res.TotalCount != 5 {
			t.Errorf("TotalCount = %d, want 5", res.TotalCount)
		}
		if len(res.Events) != 5 {
			t.Errorf("len(Events) = %d, want 5", len(res.Events))
		}
		// Events should be ordered by timestamp DESC.
		for i := 1; i < len(res.Events); i++ {
			if res.Events[i].Timestamp.After(res.Events[i-1].Timestamp) {
				t.Errorf("events not in descending order at index %d: %v > %v",
					i, res.Events[i].Timestamp, res.Events[i-1].Timestamp)
			}
		}
	})

	t.Run("offset timestamps use chronological UTC ordering", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := t.Context()
		early := time.Date(2026, 1, 1, 1, 0, 0, 0, time.FixedZone("UTC+1", 60*60))
		later := time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC)
		if _, err := s.Create(ctx, makeEvent("offset-early", "alice", model.EventTypeDeployment, early, nil)); err != nil {
			t.Fatalf("Create(offset-early) error = %v", err)
		}
		if _, err := s.Create(ctx, makeEvent("offset-later", "alice", model.EventTypeDeployment, later, nil)); err != nil {
			t.Fatalf("Create(offset-later) error = %v", err)
		}

		startAfter := time.Date(2026, 1, 1, 1, 15, 0, 0, time.FixedZone("UTC+1", 60*60))
		result, err := s.List(ctx, model.ListParams{StartAfter: &startAfter})
		if err != nil {
			t.Fatalf("List(StartAfter) error = %v", err)
		}
		if len(result.Events) != 1 || result.Events[0].ID != "offset-later" {
			t.Fatalf("List(StartAfter) events = %+v, want only offset-later", result.Events)
		}

		event, err := s.GetByID(ctx, "offset-early")
		if err != nil {
			t.Fatalf("GetByID(offset-early) error = %v", err)
		}
		if event.Timestamp.Location() != time.UTC {
			t.Errorf("GetByID(offset-early) location = %v, want UTC", event.Timestamp.Location())
		}
	})

	// Table-driven filter tests.
	filterCases := []struct {
		name          string
		params        model.ListParams
		expectedCount int
	}{
		{
			name:          "filter by StartAfter only",
			params:        model.ListParams{StartAfter: new(mustTime(t, "2026-01-03T00:00:00Z"))},
			expectedCount: 3, // list-3, list-4, list-5
		},
		{
			name:          "filter by StartBefore only",
			params:        model.ListParams{StartBefore: new(mustTime(t, "2026-01-03T00:00:00Z"))},
			expectedCount: 2, // list-1, list-2
		},
		{
			name: "filter by time range both",
			params: model.ListParams{
				StartAfter:  new(mustTime(t, "2026-01-02T00:00:00Z")),
				StartBefore: new(mustTime(t, "2026-01-04T10:00:00Z")),
			},
			expectedCount: 2, // list-2, list-3
		},
		{
			name:          "filter by UserName",
			params:        model.ListParams{UserName: "alice"},
			expectedCount: 3, // list-1, list-3, list-5
		},
		{
			name:          "filter by EventType",
			params:        model.ListParams{EventType: model.EventTypeDeployment},
			expectedCount: 3, // list-1, list-4, list-5
		},
		{
			name:          "filter by single tag",
			params:        model.ListParams{Tags: map[string]string{"env": "prod"}},
			expectedCount: 3, // list-1, list-2, list-4
		},
		{
			name:          "filter by multiple tags AND logic",
			params:        model.ListParams{Tags: map[string]string{"env": "prod", "service": "api"}},
			expectedCount: 1, // list-1 only
		},
		{
			name:          "combined filters user and type",
			params:        model.ListParams{UserName: "alice", EventType: model.EventTypeDeployment},
			expectedCount: 2, // list-1, list-5
		},
		{
			name:          "empty results with filters",
			params:        model.ListParams{UserName: "nonexistent-user"},
			expectedCount: 0,
		},
	}
	for _, tc := range filterCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := newTestStore(t)
			seedEvents(t, s)
			ctx := context.Background()

			res, err := s.List(ctx, tc.params)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if res.TotalCount != tc.expectedCount {
				t.Errorf("TotalCount = %d, want %d", res.TotalCount, tc.expectedCount)
			}
		})
	}

	t.Run("TopLevel excludes meta-events", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-02-01T10:00:00Z")
		parent := makeEvent("top-1", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		meta := &model.ChangeEvent{
			ID:        "meta-top",
			ParentID:  "top-1",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, meta); err != nil {
			t.Fatalf("Create meta: %v", err)
		}

		// Without TopLevel: both events.
		res, err := s.List(ctx, model.ListParams{})
		if err != nil {
			t.Fatalf("List all: %v", err)
		}
		if res.TotalCount != 2 {
			t.Errorf("TotalCount (all) = %d, want 2", res.TotalCount)
		}

		// With TopLevel: only the parent.
		res, err = s.List(ctx, model.ListParams{TopLevel: true})
		if err != nil {
			t.Fatalf("List TopLevel: %v", err)
		}
		if res.TotalCount != 1 {
			t.Errorf("TotalCount (TopLevel) = %d, want 1", res.TotalCount)
		}
		if len(res.Events) != 1 {
			t.Fatalf("len(Events) = %d, want 1", len(res.Events))
		}
		if res.Events[0].ID != "top-1" {
			t.Errorf("Events[0].ID = %q, want %q", res.Events[0].ID, "top-1")
		}
	})

	t.Run("pagination limit and offset", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		seedEvents(t, s)
		ctx := context.Background()

		// Page 1: first 2 events (descending by timestamp: list-5, list-4)
		res, err := s.List(ctx, model.ListParams{Limit: 2, Offset: 0})
		if err != nil {
			t.Fatalf("List page 1: %v", err)
		}
		if res.TotalCount != 5 {
			t.Errorf("TotalCount = %d, want 5", res.TotalCount)
		}
		if len(res.Events) != 2 {
			t.Fatalf("len(Events) = %d, want 2", len(res.Events))
		}
		if res.Limit != 2 {
			t.Errorf("Limit = %d, want 2", res.Limit)
		}
		if res.Offset != 0 {
			t.Errorf("Offset = %d, want 0", res.Offset)
		}

		// Page 2: next 2
		res2, err := s.List(ctx, model.ListParams{Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("List page 2: %v", err)
		}
		if len(res2.Events) != 2 {
			t.Fatalf("len(Events) = %d, want 2", len(res2.Events))
		}

		// Page 3: last 1
		res3, err := s.List(ctx, model.ListParams{Limit: 2, Offset: 4})
		if err != nil {
			t.Fatalf("List page 3: %v", err)
		}
		if len(res3.Events) != 1 {
			t.Fatalf("len(Events) = %d, want 1", len(res3.Events))
		}

		// Ensure no overlapping IDs across pages.
		seen := make(map[string]bool)
		for _, ev := range res.Events {
			seen[ev.ID] = true
		}
		for _, ev := range res2.Events {
			if seen[ev.ID] {
				t.Errorf("duplicate event %q across pages", ev.ID)
			}
			seen[ev.ID] = true
		}
		for _, ev := range res3.Events {
			if seen[ev.ID] {
				t.Errorf("duplicate event %q across pages", ev.ID)
			}
		}
	})

	t.Run("Around and Window query", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		seedEvents(t, s)
		ctx := context.Background()

		// Around 2026-01-03T10:00:00Z with a 24h window should include
		// events within [2026-01-02T10:00:00Z, 2026-01-04T10:00:00Z).
		around := mustTime(t, "2026-01-03T10:00:00Z")
		res, err := s.List(ctx, model.ListParams{
			Around: new(around),
			Window: new(24 * time.Hour),
		})
		if err != nil {
			t.Fatalf("List Around: %v", err)
		}
		// list-2 (Jan 2 10:00), list-3 (Jan 3 10:00) fall within the window.
		// list-4 (Jan 4 10:00) is at the boundary (exclusive end), so excluded.
		if res.TotalCount != 2 {
			t.Errorf("TotalCount = %d, want 2", res.TotalCount)
		}
	})

	t.Run("empty store returns empty slice", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		res, err := s.List(ctx, model.ListParams{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if res.TotalCount != 0 {
			t.Errorf("TotalCount = %d, want 0", res.TotalCount)
		}
		if res.Events == nil {
			t.Error("Events is nil, want empty slice")
		}
		if len(res.Events) != 0 {
			t.Errorf("len(Events) = %d, want 0", len(res.Events))
		}
	})

	t.Run("events have correct tags loaded", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		seedEvents(t, s)
		ctx := context.Background()

		res, err := s.List(ctx, model.ListParams{Limit: 50})
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		tagCounts := make(map[string]int)
		for _, ev := range res.Events {
			tagCounts[ev.ID] = len(ev.Tags)
		}

		expected := map[string]int{
			"list-1": 2,
			"list-2": 1,
			"list-3": 2,
			"list-4": 2,
			"list-5": 0,
		}
		for id, want := range expected {
			if tagCounts[id] != want {
				t.Errorf("event %s tag count = %d, want %d", id, tagCounts[id], want)
			}
		}
	})
}

func TestListByParentID(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	ts := mustTime(t, "2026-08-24T12:00:00Z")
	parent := makeEvent("parent", "alice", model.EventTypeDeployment, ts, nil)
	child := makeEvent("child", "bob", model.EventTypeLink, ts.Add(time.Minute), nil)
	child.ParentID = parent.ID
	child.Links = []model.EventLink{{Label: "Plan", URL: "https://notion.so/example/plan"}}
	other := makeEvent("other", "carol", model.EventTypeLink, ts.Add(2*time.Minute), nil)
	for _, event := range []*model.ChangeEvent{parent, child, other} {
		if _, err := s.Create(t.Context(), event); err != nil {
			t.Fatalf("Create(%s): %v", event.ID, err)
		}
	}

	result, err := s.List(t.Context(), model.ListParams{ParentID: parent.ID, Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.TotalCount != 1 || len(result.Events) != 1 || result.Events[0].ID != child.ID {
		t.Fatalf("List() = %+v, want child only", result)
	}
	if len(result.Events[0].Links) != 1 || result.Events[0].Links[0].Label != "Plan" {
		t.Errorf("child links = %#v", result.Events[0].Links)
	}
}

func TestListCurrent(t *testing.T) {
	t.Parallel()

	t.Run("derives active logical operations", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		base := mustTime(t, "2020-01-01T00:00:00Z")
		events := []*model.ChangeEvent{
			makeEvent("active-change", "alice", "deployment", base, map[string]string{"phase": "start", "change_id": "active"}),
			makeEvent("completed-start", "alice", "deployment", base.Add(time.Minute), map[string]string{"phase": "start", "change_id": "completed"}),
			makeEvent("completed-end", "bob", "deployment", base.Add(2*time.Minute), map[string]string{"phase": "end", "change_id": "completed"}),
			makeEvent("wrong-end-start", "alice", "deployment", base.Add(3*time.Minute), map[string]string{"phase": "start", "change_id": "still-active"}),
			makeEvent("wrong-end", "bob", "deployment", base.Add(4*time.Minute), map[string]string{"phase": "end", "change_id": "another-id"}),
			makeEvent("type-start", "alice", "maintenance", base.Add(5*time.Minute), map[string]string{"phase": "start", "change_id": "shared-id"}),
			makeEvent("type-end", "bob", "deployment", base.Add(6*time.Minute), map[string]string{"phase": "end", "change_id": "shared-id"}),
			makeEvent("legacy", "alice", "deployment", base.Add(7*time.Minute), map[string]string{"phase": "start", "deploy_id": "legacy-id"}),
			makeEvent("precedence", "alice", "deployment", base.Add(8*time.Minute), map[string]string{"phase": "start", "change_id": "canonical-id", "deploy_id": "legacy-conflict"}),
			makeEvent("legacy-conflict-end", "bob", "deployment", base.Add(9*time.Minute), map[string]string{"phase": "end", "deploy_id": "legacy-conflict"}),
			makeEvent("fallback", "alice", "deployment", base.Add(10*time.Minute), map[string]string{"phase": "start", "change_id": "", "deploy_id": "fallback-id"}),
			makeEvent("missing-id", "alice", "deployment", base.Add(11*time.Minute), map[string]string{"phase": "start"}),
			makeEvent("empty-ids", "alice", "deployment", base.Add(12*time.Minute), map[string]string{"phase": "start", "change_id": "", "deploy_id": ""}),
			makeEvent("duplicate-earliest", "alice", "deployment", base.Add(13*time.Minute), map[string]string{"phase": "start", "change_id": "duplicate"}),
			makeEvent("duplicate-later", "alice", "deployment", base.Add(14*time.Minute), map[string]string{"phase": "start", "change_id": "duplicate"}),
			makeEvent("closed-duplicate-a", "alice", "deployment", base.Add(15*time.Minute), map[string]string{"phase": "start", "change_id": "closed-duplicate"}),
			makeEvent("closed-duplicate-b", "alice", "deployment", base.Add(16*time.Minute), map[string]string{"phase": "start", "change_id": "closed-duplicate"}),
			makeEvent("closed-duplicate-end", "bob", "deployment", base.Add(17*time.Minute), map[string]string{"phase": "end", "change_id": "closed-duplicate"}),
			makeEvent("meta-parent", "alice", "deployment", base.Add(18*time.Minute), nil),
		}
		meta := makeEvent("phase-meta", "alice", "deployment", base.Add(19*time.Minute), map[string]string{"phase": "start", "change_id": "meta-id"})
		meta.ParentID = "meta-parent"
		events = append(events, meta)
		createCurrentFixtures(t, s, events...)

		result, err := s.ListCurrent(t.Context(), model.CurrentParams{})
		if err != nil {
			t.Fatalf("ListCurrent() error = %v", err)
		}

		wantIDs := []string{
			"duplicate-earliest",
			"fallback",
			"precedence",
			"legacy",
			"type-start",
			"wrong-end-start",
			"active-change",
		}
		assertEventIDs(t, result.Events, wantIDs)
		if result.TotalCount != len(wantIDs) {
			t.Errorf("TotalCount = %d, want %d", result.TotalCount, len(wantIDs))
		}
		if result.Events[0].Tags["change_id"] != "duplicate" {
			t.Errorf("representative tags = %v, want hydrated duplicate start tags", result.Events[0].Tags)
		}
	})

	t.Run("filters visibility and values after reduction", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		base := mustTime(t, "2026-08-24T10:00:00Z")
		createCurrentFixtures(
			t,
			s,
			makeEvent("payments-sev0", "alice", "deployment", base, map[string]string{"phase": "start", "change_id": "p0", "team": "payments", "scope": "service", "severity": "SEV0"}),
			makeEvent("payments-sev1", "alice", "maintenance", base.Add(time.Minute), map[string]string{"phase": "start", "change_id": "p1", "team": "payments", "scope": "system", "severity": "sev1"}),
			makeEvent("site-platform", "bob", "incident", base.Add(2*time.Minute), map[string]string{"phase": "start", "change_id": "site", "team": "platform", "scope": "site", "severity": "sev1"}),
			makeEvent("unattributed", "carol", "deployment", base.Add(3*time.Minute), map[string]string{"phase": "start", "change_id": "none", "scope": "service", "severity": "sev2"}),
			makeEvent("empty-team", "carol", "deployment", base.Add(4*time.Minute), map[string]string{"phase": "start", "change_id": "empty", "team": "", "scope": "service", "severity": "sev3"}),
			makeEvent("platform-service", "bob", "deployment", base.Add(5*time.Minute), map[string]string{"phase": "start", "change_id": "platform", "team": "platform", "scope": "service", "severity": "sev0"}),
			makeEvent("no-severity", "alice", "deployment", base.Add(6*time.Minute), map[string]string{"phase": "start", "change_id": "no-sev", "team": "payments", "scope": "service"}),
		)

		teamResult, err := s.ListCurrent(t.Context(), model.CurrentParams{ForTeam: "payments"})
		if err != nil {
			t.Fatalf("ListCurrent(ForTeam) error = %v", err)
		}
		assertEventIDs(t, teamResult.Events, []string{"no-severity", "empty-team", "unattributed", "site-platform", "payments-sev1", "payments-sev0"})

		severityResult, err := s.ListCurrent(t.Context(), model.CurrentParams{
			ForTeam:    "payments",
			Severities: []string{"sev0", "SEV1"},
		})
		if err != nil {
			t.Fatalf("ListCurrent(Severities) error = %v", err)
		}
		assertEventIDs(t, severityResult.Events, []string{"site-platform", "payments-sev1", "payments-sev0"})

		scopeResult, err := s.ListCurrent(t.Context(), model.CurrentParams{Scopes: []string{"site", "system"}})
		if err != nil {
			t.Fatalf("ListCurrent(Scopes) error = %v", err)
		}
		assertEventIDs(t, scopeResult.Events, []string{"site-platform", "payments-sev1"})

		typeResult, err := s.ListCurrent(t.Context(), model.CurrentParams{EventType: "maintenance"})
		if err != nil {
			t.Fatalf("ListCurrent(EventType) error = %v", err)
		}
		assertEventIDs(t, typeResult.Events, []string{"payments-sev1"})
	})

	t.Run("paginates the deduplicated deterministic result", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		base := mustTime(t, "2026-08-24T12:00:00Z")
		createCurrentFixtures(
			t,
			s,
			makeEvent("a", "alice", "deployment", base, map[string]string{"phase": "start", "change_id": "a"}),
			makeEvent("b", "alice", "deployment", base, map[string]string{"phase": "start", "change_id": "b"}),
			makeEvent("b-duplicate", "alice", "deployment", base.Add(time.Minute), map[string]string{"phase": "start", "change_id": "b"}),
			makeEvent("c", "alice", "deployment", base.Add(2*time.Minute), map[string]string{"phase": "start", "change_id": "c"}),
		)

		first, err := s.ListCurrent(t.Context(), model.CurrentParams{Limit: 2})
		if err != nil {
			t.Fatalf("ListCurrent(first page) error = %v", err)
		}
		assertEventIDs(t, first.Events, []string{"c", "a"})
		if first.TotalCount != 3 {
			t.Errorf("first.TotalCount = %d, want 3", first.TotalCount)
		}

		second, err := s.ListCurrent(t.Context(), model.CurrentParams{Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("ListCurrent(second page) error = %v", err)
		}
		assertEventIDs(t, second.Events, []string{"b"})
		if second.TotalCount != 3 {
			t.Errorf("second.TotalCount = %d, want 3", second.TotalCount)
		}
	})
}

func TestListCurrentByCorrelation(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	base := mustTime(t, "2026-08-24T12:00:00Z")
	for _, event := range []*model.ChangeEvent{
		makeEvent("wanted", "alice", "deployment", base, map[string]string{"phase": "start", "change_id": "wanted-id"}),
		makeEvent("other", "bob", "deployment", base.Add(time.Minute), map[string]string{"phase": "start", "change_id": "other-id"}),
	} {
		if _, err := s.Create(t.Context(), event); err != nil {
			t.Fatalf("Create(%s): %v", event.ID, err)
		}
	}
	result, err := s.ListCurrent(t.Context(), model.CurrentParams{
		EventType:        "deployment",
		CorrelationKey:   "change_id",
		CorrelationValue: "wanted-id",
		Limit:            10,
	})
	if err != nil {
		t.Fatalf("ListCurrent() error = %v", err)
	}
	assertEventIDs(t, result.Events, []string{"wanted"})
}

func createCurrentFixtures(t *testing.T, s *postgres.Store, events ...*model.ChangeEvent) {
	t.Helper()
	for _, event := range events {
		if _, err := s.Create(t.Context(), event); err != nil {
			t.Fatalf("Create(%q) error = %v", event.ID, err)
		}
	}
}

func assertEventIDs(t *testing.T, events []model.ChangeEvent, want []string) {
	t.Helper()
	got := make([]string, len(events))
	for i := range events {
		got[i] = events[i].ID
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("event IDs = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// GetAnnotations tests
// ---------------------------------------------------------------------------

func TestGetAnnotations(t *testing.T) {
	t.Parallel()

	t.Run("no annotations returns defaults", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-10T10:00:00Z")
		parent := makeEvent("ann-none", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create: %v", err)
		}

		ann, err := s.GetAnnotations(ctx, "ann-none")
		if err != nil {
			t.Fatalf("GetAnnotations: %v", err)
		}
		if ann.Starred {
			t.Error("Starred = true, want false")
		}
		if ann.Alerted {
			t.Error("Alerted = true, want false")
		}
	})

	t.Run("star then check Starred is true", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-10T10:00:00Z")
		parent := makeEvent("ann-star", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		starEvt := &model.ChangeEvent{
			ID:        "ann-star-meta",
			ParentID:  "ann-star",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, starEvt); err != nil {
			t.Fatalf("Create star: %v", err)
		}

		ann, err := s.GetAnnotations(ctx, "ann-star")
		if err != nil {
			t.Fatalf("GetAnnotations: %v", err)
		}
		if !ann.Starred {
			t.Error("Starred = false, want true")
		}
		if ann.Alerted {
			t.Error("Alerted = true, want false")
		}
	})

	t.Run("star then unstar returns Starred false", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-10T10:00:00Z")
		parent := makeEvent("ann-unstar", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		starEvt := &model.ChangeEvent{
			ID:        "ann-unstar-star",
			ParentID:  "ann-unstar",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, starEvt); err != nil {
			t.Fatalf("Create star: %v", err)
		}

		unstarEvt := &model.ChangeEvent{
			ID:        "ann-unstar-unstar",
			ParentID:  "ann-unstar",
			UserName:  "bob",
			Timestamp: ts.Add(2 * time.Minute),
			EventType: model.EventTypeUnstar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(2 * time.Minute),
		}
		if _, err := s.Create(ctx, unstarEvt); err != nil {
			t.Fatalf("Create unstar: %v", err)
		}

		ann, err := s.GetAnnotations(ctx, "ann-unstar")
		if err != nil {
			t.Fatalf("GetAnnotations: %v", err)
		}
		if ann.Starred {
			t.Error("Starred = true, want false after unstar")
		}
	})

	t.Run("alert then check Alerted is true", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-10T10:00:00Z")
		parent := makeEvent("ann-alert", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		alertEvt := &model.ChangeEvent{
			ID:        "ann-alert-meta",
			ParentID:  "ann-alert",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeAlert,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, alertEvt); err != nil {
			t.Fatalf("Create alert: %v", err)
		}

		ann, err := s.GetAnnotations(ctx, "ann-alert")
		if err != nil {
			t.Fatalf("GetAnnotations: %v", err)
		}
		if !ann.Alerted {
			t.Error("Alerted = false, want true")
		}
		if ann.Starred {
			t.Error("Starred = true, want false")
		}
	})

	t.Run("both star and alert simultaneously", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-10T10:00:00Z")
		parent := makeEvent("ann-both", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		starEvt := &model.ChangeEvent{
			ID:        "ann-both-star",
			ParentID:  "ann-both",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, starEvt); err != nil {
			t.Fatalf("Create star: %v", err)
		}

		alertEvt := &model.ChangeEvent{
			ID:        "ann-both-alert",
			ParentID:  "ann-both",
			UserName:  "carol",
			Timestamp: ts.Add(2 * time.Minute),
			EventType: model.EventTypeAlert,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(2 * time.Minute),
		}
		if _, err := s.Create(ctx, alertEvt); err != nil {
			t.Fatalf("Create alert: %v", err)
		}

		ann, err := s.GetAnnotations(ctx, "ann-both")
		if err != nil {
			t.Fatalf("GetAnnotations: %v", err)
		}
		if !ann.Starred {
			t.Error("Starred = false, want true")
		}
		if !ann.Alerted {
			t.Error("Alerted = false, want true")
		}
	})
}

// ---------------------------------------------------------------------------
// GetAnnotationsBatch tests
// ---------------------------------------------------------------------------

func TestGetAnnotationsBatch(t *testing.T) {
	t.Parallel()

	t.Run("multiple events with different annotations", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-15T10:00:00Z")

		// Create three parent events.
		ev1 := makeEvent("batch-1", "alice", model.EventTypeDeployment, ts, nil)
		ev2 := makeEvent("batch-2", "bob", model.EventTypeDeployment, ts.Add(time.Hour), nil)
		ev3 := makeEvent("batch-3", "carol", model.EventTypeDeployment, ts.Add(2*time.Hour), nil)
		for _, ev := range []*model.ChangeEvent{ev1, ev2, ev3} {
			if _, err := s.Create(ctx, ev); err != nil {
				t.Fatalf("Create %s: %v", ev.ID, err)
			}
		}

		// Star batch-1.
		starEvt := &model.ChangeEvent{
			ID:        "batch-1-star",
			ParentID:  "batch-1",
			UserName:  "bob",
			Timestamp: ts.Add(3 * time.Hour),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(3 * time.Hour),
		}
		if _, err := s.Create(ctx, starEvt); err != nil {
			t.Fatalf("Create star: %v", err)
		}

		// Alert batch-2.
		alertEvt := &model.ChangeEvent{
			ID:        "batch-2-alert",
			ParentID:  "batch-2",
			UserName:  "carol",
			Timestamp: ts.Add(4 * time.Hour),
			EventType: model.EventTypeAlert,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(4 * time.Hour),
		}
		if _, err := s.Create(ctx, alertEvt); err != nil {
			t.Fatalf("Create alert: %v", err)
		}

		// batch-3 has no annotations.

		result, err := s.GetAnnotationsBatch(ctx, []string{"batch-1", "batch-2", "batch-3"})
		if err != nil {
			t.Fatalf("GetAnnotationsBatch: %v", err)
		}

		if len(result) != 3 {
			t.Fatalf("len(result) = %d, want 3", len(result))
		}

		// batch-1: starred, not alerted.
		if !result["batch-1"].Starred {
			t.Error("batch-1 Starred = false, want true")
		}
		if result["batch-1"].Alerted {
			t.Error("batch-1 Alerted = true, want false")
		}

		// batch-2: not starred, alerted.
		if result["batch-2"].Starred {
			t.Error("batch-2 Starred = true, want false")
		}
		if !result["batch-2"].Alerted {
			t.Error("batch-2 Alerted = false, want true")
		}

		// batch-3: neither starred nor alerted.
		if result["batch-3"].Starred {
			t.Error("batch-3 Starred = true, want false")
		}
		if result["batch-3"].Alerted {
			t.Error("batch-3 Alerted = true, want false")
		}
	})

	t.Run("empty input returns empty map", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		result, err := s.GetAnnotationsBatch(ctx, []string{})
		if err != nil {
			t.Fatalf("GetAnnotationsBatch: %v", err)
		}
		if len(result) != 0 {
			t.Errorf("len(result) = %d, want 0", len(result))
		}
	})
}
