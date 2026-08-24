package main

import (
	"bufio"
	"database/sql"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type oneConnListener struct {
	conn      net.Conn
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	accepted  bool
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.accepted {
		l.accepted = true
		conn := l.conn
		l.mu.Unlock()
		return conn, nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func TestServeUntilShutdownWaitsForActiveRequest(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseRequest:
		default:
			close(releaseRequest)
		}
	})

	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(requestStarted)
			<-releaseRequest
			_, _ = io.WriteString(w, "done")
		}),
	}
	serverConn, clientConn := net.Pipe()
	listener := &oneConnListener{conn: serverConn, closed: make(chan struct{})}
	t.Cleanup(func() { _ = clientConn.Close() })
	t.Cleanup(func() { _ = srv.Close() })

	shutdownCh := make(chan os.Signal, 1)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveUntilShutdown(srv, listener, shutdownCh, 5*time.Second)
	}()

	requestDone := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://pipe/", nil)
		if err == nil {
			req.Close = true
			err = req.Write(clientConn)
		}
		var resp *http.Response
		if err == nil {
			resp, err = http.ReadResponse(bufio.NewReader(clientConn), req)
		}
		if err == nil {
			_, readErr := io.Copy(io.Discard, resp.Body)
			closeErr := resp.Body.Close()
			err = errors.Join(readErr, closeErr)
		}
		requestDone <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach handler")
	}
	shutdownCh <- syscall.SIGTERM

	select {
	case err := <-serveDone:
		t.Fatalf("serveUntilShutdown() returned before handler completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseRequest)
	if err := <-requestDone; err != nil {
		t.Fatalf("request error = %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("serveUntilShutdown() error = %v", err)
	}
}

func TestRunMigrationsRefusesDirtyDatabase(t *testing.T) {
	t.Parallel()

	db := openMigrationTestDB(t)
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE schema_migrations (version uint64, dirty bool);
		INSERT INTO schema_migrations (version, dirty) VALUES (1, 1);
	`); err != nil {
		t.Fatalf("seed dirty migration state: %v", err)
	}

	err := runMigrations(db)
	if err == nil {
		t.Fatal("runMigrations() error = nil, want dirty-migration error")
	}
	if !strings.Contains(err.Error(), "dirty at version 1") {
		t.Fatalf("runMigrations() error = %q, want dirty version context", err)
	}

	var version int
	var dirty bool
	if err := db.QueryRowContext(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("read migration state: %v", err)
	}
	if version != 1 || !dirty {
		t.Errorf("migration state = (version=%d, dirty=%t), want (version=1, dirty=true)", version, dirty)
	}
}

func TestRunMigrationsCreatesFreshSchema(t *testing.T) {
	t.Parallel()

	db := openMigrationTestDB(t)
	ctx := t.Context()
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() error = %v", err)
	}

	var externalIDColumns int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pragma_table_info('change_events') WHERE name = 'external_id'
	`).Scan(&externalIDColumns); err != nil {
		t.Fatalf("inspect change_events schema: %v", err)
	}
	if externalIDColumns != 1 {
		t.Errorf("external_id column count = %d, want 1", externalIDColumns)
	}

	var externalIDIndexes int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_change_events_external_id'
	`).Scan(&externalIDIndexes); err != nil {
		t.Fatalf("inspect external_id index: %v", err)
	}
	if externalIDIndexes != 1 {
		t.Errorf("external_id index count = %d, want 1", externalIDIndexes)
	}

	var linkTables int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'change_event_links'
	`).Scan(&linkTables); err != nil {
		t.Fatalf("inspect change_event_links table: %v", err)
	}
	if linkTables != 1 {
		t.Errorf("change_event_links table count = %d, want 1", linkTables)
	}

	var version int
	var dirty bool
	if err := db.QueryRowContext(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("read migration state: %v", err)
	}
	if version != 2 || dirty {
		t.Errorf("migration state = (version=%d, dirty=%t), want (version=2, dirty=false)", version, dirty)
	}
}

func openMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "migration.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})
	return db
}
