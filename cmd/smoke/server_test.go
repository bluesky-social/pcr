package main

import "testing"

func TestDatabaseURLWithSearchPath(t *testing.T) {
	t.Parallel()
	got, err := databaseURLWithSearchPath(
		"postgres://pcr@db.internal/pcr?sslmode=require&application_name=pcr-smoke",
		"pcr_smoke_abc123",
	)
	if err != nil {
		t.Fatalf("databaseURLWithSearchPath(): %v", err)
	}
	want := "postgres://pcr@db.internal/pcr?application_name=pcr-smoke&search_path=pcr_smoke_abc123&sslmode=require"
	if got != want {
		t.Errorf("databaseURLWithSearchPath() = %q, want %q", got, want)
	}
}

func TestDatabaseURLWithSearchPathRejectsInvalidURL(t *testing.T) {
	t.Parallel()
	if _, err := databaseURLWithSearchPath("://invalid", "pcr_smoke_abc123"); err == nil {
		t.Fatal("databaseURLWithSearchPath() error = nil, want parse error")
	}
}
