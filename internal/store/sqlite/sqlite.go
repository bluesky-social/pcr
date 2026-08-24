package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/store"
)

// Compile-time interface check.
var _ store.ChangeStore = (*Store)(nil)

// Store is a SQLite-backed implementation of store.ChangeStore.
type Store struct {
	db                 *sql.DB
	slowQueryThreshold time.Duration
	toggleMu           sync.Mutex
}

// New wraps an existing *sql.DB connection as a Store.
// slowQueryThreshold sets the duration above which store operations are logged at Warn level.
func New(db *sql.DB, slowQueryThreshold time.Duration) *Store {
	return &Store{
		db:                 db,
		slowQueryThreshold: slowQueryThreshold,
	}
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// logOperation logs the duration of a store operation. If the duration exceeds
// the slow query threshold, it logs at Warn level; otherwise at Debug level.
func (s *Store) logOperation(ctx context.Context, op string, start time.Time, err error) {
	duration := time.Since(start)
	attrs := []slog.Attr{
		slog.String("op", op),
		slog.Duration("duration", duration),
	}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}

	if duration >= s.slowQueryThreshold {
		slog.LogAttrs(
			ctx,
			slog.LevelWarn,
			"slow store operation",
			attrs...,
		)
		return
	}

	slog.LogAttrs(
		ctx,
		slog.LevelDebug,
		"store operation",
		attrs...,
	)
}

// closeQuiet releases a closer in a deferred call and logs any error at warn
// level. It exists so callers don't have to choose between dropping Close
// errors silently (errcheck violation) and inlining the same boilerplate at
// every defer site. The op label identifies which Store method is closing.
func closeQuiet(ctx context.Context, op string, c io.Closer) {
	if err := c.Close(); err != nil {
		slog.WarnContext(ctx, "store close error", "op", op, "error", err)
	}
}

// Create inserts a new change event and its tags within a transaction.
func (s *Store) Create(ctx context.Context, event *model.ChangeEvent) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "Create", start, err) }()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	// Rollback after a successful Commit is a documented no-op (sql.ErrTxDone);
	// the error is unactionable in either case.
	defer tx.Rollback() //nolint:errcheck // safe: rollback after successful commit returns sql.ErrTxDone

	var parentID *string
	if event.ParentID != "" {
		parentID = &event.ParentID
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO change_events (id, external_id, parent_id, user_name, timestamp, event_type, description, long_description, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		nullableString(event.ExternalID),
		parentID,
		event.UserName,
		formatTimestamp(event.Timestamp),
		event.EventType,
		event.Description,
		event.LongDescription,
		formatTimestamp(event.CreatedAt),
	)
	if err != nil && event.ExternalID != "" && isUniqueViolation(err) {
		// Event with this external_id already exists — return it (idempotent).
		// Rollback error is unactionable here; we already have the duplicate-
		// detection result and are about to return.
		_ = tx.Rollback()
		existing, lookupErr := s.GetByExternalID(ctx, event.ExternalID)
		if lookupErr != nil {
			return nil, lookupErr
		}
		return existing, store.ErrDuplicate
	}
	if err != nil {
		return nil, fmt.Errorf("insert event: %w", err)
	}

	if err := insertTags(ctx, tx, event.ID, event.Tags); err != nil {
		return nil, err
	}
	if err := insertLinks(ctx, tx, event.ID, event.Links); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	result = &model.ChangeEvent{
		ID:              event.ID,
		ExternalID:      event.ExternalID,
		ParentID:        event.ParentID,
		UserName:        event.UserName,
		Timestamp:       canonicalTime(event.Timestamp),
		EventType:       event.EventType,
		Description:     event.Description,
		LongDescription: event.LongDescription,
		Links:           slices.Clone(event.Links),
		Tags:            maps.Clone(event.Tags),
		CreatedAt:       canonicalTime(event.CreatedAt),
	}
	return result, nil
}

// ToggleStar atomically appends the opposite star transition.
func (s *Store) ToggleStar(ctx context.Context, eventID, userName string) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "ToggleStar", start, err) }()
	return s.toggleTransition(ctx, eventID, userName, model.EventTypeStar, model.EventTypeUnstar, "starred", "unstarred")
}

// ToggleAlert atomically appends the opposite alert transition.
func (s *Store) ToggleAlert(ctx context.Context, eventID, userName string) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "ToggleAlert", start, err) }()
	return s.toggleTransition(ctx, eventID, userName, model.EventTypeAlert, model.EventTypeClearAlert, "alert opened", "alert cleared")
}

// toggleTransition serializes a read-latest/append-opposite state change. The
// process-local mutex avoids unnecessary SQLITE_BUSY retries between requests;
// the immediate transaction configured by the caller supplies the DB lock.
func (s *Store) toggleTransition(
	ctx context.Context,
	eventID, userName, activeType, inactiveType, activeDescription, inactiveDescription string,
) (result *model.ChangeEvent, err error) {

	s.toggleMu.Lock()
	defer s.toggleMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // safe: rollback after successful commit returns sql.ErrTxDone

	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM change_events WHERE id = ?`, eventID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("check parent event: %w", err)
	}

	eventType := activeType
	description := activeDescription
	var latestType string
	err = tx.QueryRowContext(
		ctx,
		`SELECT event_type FROM change_events
		 WHERE parent_id = ? AND event_type IN (?, ?)
		 ORDER BY rowid DESC LIMIT 1`,
		eventID,
		activeType,
		inactiveType,
	).Scan(&latestType)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read transition state: %w", err)
	}
	if err == nil && latestType == activeType {
		eventType = inactiveType
		description = inactiveDescription
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate event ID: %w", err)
	}
	now := canonicalTime(time.Now())
	result = &model.ChangeEvent{
		ID:          id.String(),
		ParentID:    eventID,
		UserName:    userName,
		Timestamp:   now,
		EventType:   eventType,
		Description: description,
		Tags:        make(map[string]string),
		CreatedAt:   now,
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO change_events
		 (id, external_id, parent_id, user_name, timestamp, event_type, description, long_description, created_at)
		 VALUES (?, NULL, ?, ?, ?, ?, ?, '', ?)`,
		result.ID,
		result.ParentID,
		result.UserName,
		formatTimestamp(result.Timestamp),
		result.EventType,
		result.Description,
		formatTimestamp(result.CreatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("insert transition: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	return result, nil
}

// GetByID retrieves a single change event by ID, including its tags.
// Returns (nil, nil) when the event is not found.
func (s *Store) GetByID(ctx context.Context, id string) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "GetByID", start, err) }()

	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, external_id, parent_id, user_name, timestamp, event_type, description, long_description, created_at
		 FROM change_events WHERE id = ?`,
		id,
	)

	ev, err := scanEvent(row)
	if err != nil {
		return nil, err
	}
	if ev == nil {
		return nil, nil
	}

	tags, err := s.loadTagsForEvents(ctx, []string{ev.ID})
	if err != nil {
		return nil, err
	}
	ev.Tags = tags[ev.ID]
	links, err := s.loadLinksForEvents(ctx, []string{ev.ID})
	if err != nil {
		return nil, err
	}
	ev.Links = links[ev.ID]

	return ev, nil
}

// List queries change events with optional filters and pagination.
func (s *Store) List(ctx context.Context, params model.ListParams) (result *model.ListResult, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "List", start, err) }()

	where, args := buildWhereClause(params)
	limit := params.EffectiveLimit()

	// Count total matching rows.
	// `where` comes from buildWhereClause and is composed only of constant
	// SQL fragments containing `?` placeholders; user input is bound via
	// the args slice passed to QueryRowContext, never interpolated.
	//nolint:gosec // G201: SQL fragments are constants; user input bound via parameter placeholders
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM change_events%s", where)
	var totalCount int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("count events: %w", err)
	}

	// Fetch the page. Same constraint as countQuery above: user input is
	// bound via parameters, only the WHERE clause shape is interpolated.
	//nolint:gosec // G201: SQL fragments are constants; user input bound via parameter placeholders
	selectQuery := fmt.Sprintf(
		`SELECT id, external_id, parent_id, user_name, timestamp, event_type, description, long_description, created_at
		 FROM change_events%s
		 ORDER BY timestamp DESC, id ASC
		 LIMIT ? OFFSET ?`,
		where,
	)

	fetchArgs := make([]any, 0, len(args)+2)
	fetchArgs = append(fetchArgs, args...)
	fetchArgs = append(fetchArgs, limit, params.Offset)
	rows, err := s.db.QueryContext(ctx, selectQuery, fetchArgs...) //nolint:sqlclosecheck // closed via deferred closeQuiet helper below
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer closeQuiet(ctx, "List", rows)

	events := make([]model.ChangeEvent, 0)
	eventIDs := make([]string, 0)
	for rows.Next() {
		ev, err := scanEventFromRows(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, *ev)
		eventIDs = append(eventIDs, ev.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	// Load related records for all returned events in bounded queries.
	if len(eventIDs) > 0 {
		tagMap, err := s.loadTagsForEvents(ctx, eventIDs)
		if err != nil {
			return nil, err
		}
		for i := range events {
			events[i].Tags = tagMap[events[i].ID]
		}
		linkMap, err := s.loadLinksForEvents(ctx, eventIDs)
		if err != nil {
			return nil, err
		}
		for i := range events {
			events[i].Links = linkMap[events[i].ID]
		}
	}

	return &model.ListResult{
		Events:     events,
		TotalCount: totalCount,
		Limit:      limit,
		Offset:     params.Offset,
	}, nil
}

const currentCTEs = `WITH phase_event AS (
	SELECT tag.event_id AS id,
	       event.event_type,
	       event.timestamp,
	       tag.value AS phase
	FROM change_event_tags AS tag
	JOIN change_events AS event ON event.id = tag.event_id
	WHERE tag.key = 'phase'
	  AND tag.value IN ('start', 'end')
	  AND event.parent_id IS NULL
),
correlated AS MATERIALIZED (
	SELECT phase_event.id,
	       phase_event.event_type,
	       phase_event.timestamp,
	       phase_event.phase,
	       CASE
	           WHEN NULLIF(change_tag.value, '') IS NOT NULL THEN 'change_id'
	           ELSE 'deploy_id'
	       END AS correlation_key,
	       COALESCE(
	           NULLIF(change_tag.value, ''),
	           NULLIF(deploy_tag.value, '')
	       ) AS correlation_value
	FROM phase_event
	LEFT JOIN change_event_tags AS change_tag
	       ON change_tag.event_id = phase_event.id AND change_tag.key = 'change_id'
	LEFT JOIN change_event_tags AS deploy_tag
	       ON deploy_tag.event_id = phase_event.id AND deploy_tag.key = 'deploy_id'
),
ended AS MATERIALIZED (
	SELECT DISTINCT event_type, correlation_key, correlation_value
	FROM correlated
	WHERE phase = 'end'
	  AND correlation_value IS NOT NULL
),
active AS (
	SELECT candidate.id,
	       candidate.timestamp,
	       candidate.correlation_key,
	       candidate.correlation_value,
	       ROW_NUMBER() OVER (
	           PARTITION BY candidate.event_type,
	                        candidate.correlation_key,
	                        candidate.correlation_value
	           ORDER BY candidate.timestamp ASC, candidate.id ASC
	       ) AS representative_rank
	FROM correlated AS candidate
	LEFT JOIN ended
	       ON ended.event_type = candidate.event_type
	      AND ended.correlation_key = candidate.correlation_key
	      AND ended.correlation_value = candidate.correlation_value
	WHERE candidate.phase = 'start'
	  AND candidate.correlation_value IS NOT NULL
	  AND ended.correlation_value IS NULL
)
`

const currentFrom = `FROM active
JOIN change_events AS event ON event.id = active.id
LEFT JOIN change_event_tags AS team
	ON team.event_id = active.id AND team.key = 'team'
LEFT JOIN change_event_tags AS scope
	ON scope.event_id = active.id AND scope.key = 'scope'
LEFT JOIN change_event_tags AS severity
	ON severity.event_id = active.id AND severity.key = 'severity'`

// ListCurrent returns one representative start event for each logical operation
// that has no matching end event. Filtering and pagination apply after reduction.
func (s *Store) ListCurrent(ctx context.Context, params model.CurrentParams) (result *model.ListResult, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "ListCurrent", start, err) }()

	where, args := buildCurrentWhereClause(params)
	limit := params.EffectiveLimit()

	// SQL fragments are constants and user input is passed only as bound values.
	//nolint:gosec // G201: constant query shape with parameter placeholders
	countQuery := fmt.Sprintf("%sSELECT COUNT(*) %s%s", currentCTEs, currentFrom, where)
	var totalCount int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("count current events: %w", err)
	}

	//nolint:gosec // G201: constant query shape with parameter placeholders
	selectQuery := fmt.Sprintf(
		`%sSELECT event.id, event.external_id, event.parent_id, event.user_name, event.timestamp,
		       event.event_type, event.description, event.long_description, event.created_at
		%s%s
		ORDER BY event.timestamp DESC, event.id ASC
		LIMIT ? OFFSET ?`,
		currentCTEs,
		currentFrom,
		where,
	)
	fetchArgs := make([]any, 0, len(args)+2)
	fetchArgs = append(fetchArgs, args...)
	fetchArgs = append(fetchArgs, limit, params.Offset)

	rows, err := s.db.QueryContext(ctx, selectQuery, fetchArgs...) //nolint:sqlclosecheck // closed by deferred closeQuiet
	if err != nil {
		return nil, fmt.Errorf("list current events: %w", err)
	}
	defer closeQuiet(ctx, "ListCurrent", rows)

	events := make([]model.ChangeEvent, 0)
	eventIDs := make([]string, 0)
	for rows.Next() {
		event, err := scanEventFromRows(rows)
		if err != nil {
			return nil, fmt.Errorf("scan current event: %w", err)
		}
		events = append(events, *event)
		eventIDs = append(eventIDs, event.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("current rows iteration: %w", err)
	}

	if len(eventIDs) > 0 {
		tagMap, err := s.loadTagsForEvents(ctx, eventIDs)
		if err != nil {
			return nil, err
		}
		for i := range events {
			events[i].Tags = tagMap[events[i].ID]
		}
		linkMap, err := s.loadLinksForEvents(ctx, eventIDs)
		if err != nil {
			return nil, err
		}
		for i := range events {
			events[i].Links = linkMap[events[i].ID]
		}
	}

	return &model.ListResult{
		Events:     events,
		TotalCount: totalCount,
		Limit:      limit,
		Offset:     params.Offset,
	}, nil
}

// GetAnnotations returns the derived annotation state (starred, alerted) for a
// single event by walking its meta-events in reverse chronological order.
//
// Complexity comes from the chronological walk plus four meta-event types
// (star/unstar, alert/clear-alert) each gated on whether their resolution
// is still pending. Splitting the loop body out hides the state machine.
//
//nolint:gocognit // meta-event resolution loop; complexity is inherent to the state machine
func (s *Store) GetAnnotations(ctx context.Context, eventID string) (result *model.EventAnnotations, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "GetAnnotations", start, err) }()

	rows, err := s.db.QueryContext( //nolint:sqlclosecheck // closed via deferred closeQuiet helper below
		ctx,
		`SELECT event_type FROM change_events
		 WHERE parent_id = ? AND event_type IN ('star', 'unstar', 'alert', 'clear-alert')
		 ORDER BY rowid DESC`,
		eventID,
	)
	if err != nil {
		return nil, fmt.Errorf("query annotations: %w", err)
	}
	defer closeQuiet(ctx, "GetAnnotations", rows)

	annotations := &model.EventAnnotations{}
	starResolved := false
	alertResolved := false

	for rows.Next() {
		if starResolved && alertResolved {
			break
		}

		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			return nil, fmt.Errorf("scan annotation: %w", err)
		}

		switch eventType {
		case "star":
			if !starResolved {
				annotations.Starred = true
				starResolved = true
			}
		case "unstar":
			if !starResolved {
				annotations.Starred = false
				starResolved = true
			}
		case "alert":
			if !alertResolved {
				annotations.Alerted = true
				alertResolved = true
			}
		case "clear-alert":
			if !alertResolved {
				annotations.Alerted = false
				alertResolved = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return annotations, nil
}

// GetAnnotationsBatch returns the derived annotation state for multiple events.
// It runs the same per-event resolution as GetAnnotations, fanned out across a
// shared rows iteration with per-parent state tracking -- avoiding N+1 queries.
//
//nolint:gocognit,funlen // batched variant of GetAnnotations; same state machine, plus per-parent tracking
func (s *Store) GetAnnotationsBatch(ctx context.Context, eventIDs []string) (result map[string]*model.EventAnnotations, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "GetAnnotationsBatch", start, err) }()

	if len(eventIDs) == 0 {
		return make(map[string]*model.EventAnnotations), nil
	}

	placeholders := make([]string, len(eventIDs))
	args := make([]any, len(eventIDs))
	for i, id := range eventIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// Only the IN-list placeholder count is interpolated; the placeholders
	// themselves are literal `?` and the eventIDs are bound via the args slice.
	//nolint:gosec // G201: only `?` placeholder count is interpolated; user input bound via parameter placeholders
	query := fmt.Sprintf(
		`SELECT parent_id, event_type FROM change_events
		 WHERE parent_id IN (%s) AND event_type IN ('star', 'unstar', 'alert', 'clear-alert')
		 ORDER BY rowid DESC`,
		strings.Join(placeholders, ","),
	)

	rows, err := s.db.QueryContext(ctx, query, args...) //nolint:sqlclosecheck // closed via deferred closeQuiet helper below
	if err != nil {
		return nil, fmt.Errorf("query annotations batch: %w", err)
	}
	defer closeQuiet(ctx, "GetAnnotationsBatch", rows)

	// Track which annotations have been resolved per parent.
	type resolvedState struct {
		starResolved  bool
		alertResolved bool
	}
	resolved := make(map[string]*resolvedState)
	annotations := make(map[string]*model.EventAnnotations)

	// Initialize entries for all requested IDs.
	for _, id := range eventIDs {
		annotations[id] = &model.EventAnnotations{}
		resolved[id] = &resolvedState{}
	}

	for rows.Next() {
		var parentID, eventType string
		if err := rows.Scan(&parentID, &eventType); err != nil {
			return nil, fmt.Errorf("scan annotation: %w", err)
		}

		state := resolved[parentID]
		if state == nil {
			continue
		}

		switch eventType {
		case "star":
			if !state.starResolved {
				annotations[parentID].Starred = true
				state.starResolved = true
			}
		case "unstar":
			if !state.starResolved {
				annotations[parentID].Starred = false
				state.starResolved = true
			}
		case "alert":
			if !state.alertResolved {
				annotations[parentID].Alerted = true
				state.alertResolved = true
			}
		case "clear-alert":
			if !state.alertResolved {
				annotations[parentID].Alerted = false
				state.alertResolved = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return annotations, nil
}

// GetByExternalID retrieves a single change event by its external_id, including its tags.
// Returns (nil, nil) when no event with the given external_id exists.
func (s *Store) GetByExternalID(ctx context.Context, externalID string) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "GetByExternalID", start, err) }()

	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, external_id, parent_id, user_name, timestamp, event_type, description, long_description, created_at
		 FROM change_events WHERE external_id = ?`,
		externalID,
	)

	ev, err := scanEvent(row)
	if err != nil {
		return nil, err
	}
	if ev == nil {
		return nil, nil
	}

	tags, err := s.loadTagsForEvents(ctx, []string{ev.ID})
	if err != nil {
		return nil, err
	}
	ev.Tags = tags[ev.ID]
	links, err := s.loadLinksForEvents(ctx, []string{ev.ID})
	if err != nil {
		return nil, err
	}
	ev.Links = links[ev.ID]

	return ev, nil
}

// --- helpers ---

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanEventFields scans 9 columns from a change_events row into a ChangeEvent.
func scanEventFields(sc scanner) (*model.ChangeEvent, error) {
	var ev model.ChangeEvent
	var externalID *string
	var parentID *string
	var timestamp, createdAt string

	err := sc.Scan(
		&ev.ID,
		&externalID,
		&parentID,
		&ev.UserName,
		&timestamp,
		&ev.EventType,
		&ev.Description,
		&ev.LongDescription,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}

	// Convert nullable external_id to string (empty when NULL).
	if externalID != nil {
		ev.ExternalID = *externalID
	}

	// Convert nullable parent_id to string (empty when NULL).
	if parentID != nil {
		ev.ParentID = *parentID
	}

	var parseErr error
	ev.Timestamp, parseErr = time.Parse(time.RFC3339, timestamp)
	if parseErr != nil {
		return nil, fmt.Errorf("parse timestamp: %w", parseErr)
	}

	ev.CreatedAt, parseErr = time.Parse(time.RFC3339, createdAt)
	if parseErr != nil {
		return nil, fmt.Errorf("parse created_at: %w", parseErr)
	}

	return &ev, nil
}

// scanEvent scans from a *sql.Row, returning (nil, nil) on ErrNoRows.
func scanEvent(row *sql.Row) (*model.ChangeEvent, error) {
	ev, err := scanEventFields(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan event: %w", err)
	}
	return ev, nil
}

// scanEventFromRows scans from *sql.Rows (the cursor is already on a valid row).
func scanEventFromRows(rows *sql.Rows) (*model.ChangeEvent, error) {
	return scanEventFields(rows)
}

// insertTags inserts all tags for an event within the given transaction.
func insertTags(ctx context.Context, tx *sql.Tx, eventID string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}

	stmt, err := tx.PrepareContext( //nolint:sqlclosecheck // closed via deferred closeQuiet helper below
		ctx,
		`INSERT INTO change_event_tags (event_id, key, value) VALUES (?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("prepare insert tags: %w", err)
	}
	defer closeQuiet(ctx, "insertTags", stmt)

	for k, v := range tags {
		if _, err := stmt.ExecContext(ctx, eventID, k, v); err != nil {
			return fmt.Errorf("insert tag %q: %w", k, err)
		}
	}

	return nil
}

func insertLinks(ctx context.Context, tx *sql.Tx, eventID string, links []model.EventLink) error {
	if len(links) == 0 {
		return nil
	}

	stmt, err := tx.PrepareContext( //nolint:sqlclosecheck // closed via deferred closeQuiet below
		ctx,
		`INSERT INTO change_event_links (event_id, position, label, url) VALUES (?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("prepare insert links: %w", err)
	}
	defer closeQuiet(ctx, "insertLinks", stmt)

	for position, link := range links {
		if _, err := stmt.ExecContext(ctx, eventID, position, link.Label, link.URL); err != nil {
			return fmt.Errorf("insert link %d: %w", position, err)
		}
	}
	return nil
}

// loadTagsForEvents fetches tags for the given event IDs in one query.
func (s *Store) loadTagsForEvents(ctx context.Context, ids []string) (map[string]map[string]string, error) {
	if len(ids) == 0 {
		return make(map[string]map[string]string), nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	// Only the IN-list placeholder count is interpolated; ids are bound
	// via the args slice.
	//nolint:gosec // G201: only `?` placeholder count is interpolated; user input bound via parameter placeholders
	query := fmt.Sprintf(
		`SELECT event_id, key, value FROM change_event_tags WHERE event_id IN (%s)`,
		strings.Join(placeholders, ","),
	)

	rows, err := s.db.QueryContext(ctx, query, args...) //nolint:sqlclosecheck // closed via deferred closeQuiet helper below
	if err != nil {
		return nil, fmt.Errorf("load tags: %w", err)
	}
	defer closeQuiet(ctx, "loadTagsForEvents", rows)

	result := make(map[string]map[string]string)
	for rows.Next() {
		var eventID, key, value string
		if err := rows.Scan(&eventID, &key, &value); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		if result[eventID] == nil {
			result[eventID] = make(map[string]string)
		}
		result[eventID][key] = value
	}

	return result, rows.Err()
}

func (s *Store) loadLinksForEvents(ctx context.Context, ids []string) (map[string][]model.EventLink, error) {
	if len(ids) == 0 {
		return make(map[string][]model.EventLink), nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	//nolint:gosec // G201: only `?` placeholder count is interpolated; IDs are bound values
	query := fmt.Sprintf(
		`SELECT event_id, label, url FROM change_event_links WHERE event_id IN (%s) ORDER BY event_id, position`,
		strings.Join(placeholders, ","),
	)
	rows, err := s.db.QueryContext(ctx, query, args...) //nolint:sqlclosecheck // closed by closeQuiet
	if err != nil {
		return nil, fmt.Errorf("load links: %w", err)
	}
	defer closeQuiet(ctx, "loadLinksForEvents", rows)

	result := make(map[string][]model.EventLink)
	for rows.Next() {
		var eventID string
		var link model.EventLink
		if err := rows.Scan(&eventID, &link.Label, &link.URL); err != nil {
			return nil, fmt.Errorf("scan link: %w", err)
		}
		result[eventID] = append(result[eventID], link)
	}
	return result, rows.Err()
}

// buildWhereClause constructs the WHERE clause and parameter list for List queries.
func buildWhereClause(params model.ListParams) (string, []any) {
	clauses := make([]string, 0)
	args := make([]any, 0)

	if params.ParentID != "" {
		clauses = append(clauses, "parent_id = ?")
		args = append(args, params.ParentID)
	}

	// Around+Window takes precedence over StartAfter/StartBefore when set.
	if params.Around != nil && params.Window != nil && *params.Window > 0 {
		windowStart := params.Around.Add(-*params.Window)
		windowEnd := params.Around.Add(*params.Window)
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, formatTimestamp(windowStart))
		clauses = append(clauses, "timestamp < ?")
		args = append(args, formatTimestamp(windowEnd))
	} else {
		if params.StartAfter != nil {
			clauses = append(clauses, "timestamp >= ?")
			args = append(args, formatTimestamp(*params.StartAfter))
		}

		if params.StartBefore != nil {
			clauses = append(clauses, "timestamp < ?")
			args = append(args, formatTimestamp(*params.StartBefore))
		}
	}

	if params.UserName != "" {
		clauses = append(clauses, "user_name = ?")
		args = append(args, params.UserName)
	}

	if params.EventType != "" {
		clauses = append(clauses, "event_type = ?")
		args = append(args, params.EventType)
	}
	if params.TopLevel {
		clauses = append(clauses, "parent_id IS NULL")
	}

	if params.AlertedOnly {
		// Find events whose most recent alert/clear-alert meta-event is "alert".
		// This subquery gets parent IDs where the latest annotation is an active alert.
		clauses = append(clauses, `id IN (
			SELECT parent_id FROM change_events AS meta
			WHERE meta.event_type IN ('alert', 'clear-alert')
			AND meta.parent_id IS NOT NULL
			AND NOT EXISTS (
				SELECT 1 FROM change_events AS newer
				WHERE newer.parent_id = meta.parent_id
				AND newer.event_type IN ('alert', 'clear-alert')
				AND newer.rowid > meta.rowid
			)
			AND meta.event_type = 'alert'
		)`)
	}

	if len(params.Tags) > 0 {
		tagClauses := make([]string, 0, len(params.Tags))
		for k, v := range params.Tags {
			tagClauses = append(tagClauses, "(key = ? AND value = ?)")
			args = append(args, k, v)
		}
		subquery := fmt.Sprintf(
			"id IN (SELECT event_id FROM change_event_tags WHERE %s GROUP BY event_id HAVING COUNT(DISTINCT key) = ?)",
			strings.Join(tagClauses, " OR "),
		)
		clauses = append(clauses, subquery)
		args = append(args, len(params.Tags))
	}

	if len(clauses) == 0 {
		return "", make([]any, 0)
	}

	return " WHERE " + strings.Join(clauses, " AND "), args
}

func buildCurrentWhereClause(params model.CurrentParams) (string, []any) {
	clauses := []string{"active.representative_rank = 1"}
	args := make([]any, 0)

	if params.ForTeam != "" {
		clauses = append(clauses, "(team.value = ? OR NULLIF(team.value, '') IS NULL OR scope.value = 'site')")
		args = append(args, params.ForTeam)
	}
	if len(params.Scopes) > 0 {
		placeholders := make([]string, len(params.Scopes))
		for i, scope := range params.Scopes {
			placeholders[i] = "?"
			args = append(args, scope)
		}
		clauses = append(clauses, "scope.value IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(params.Severities) > 0 {
		placeholders := make([]string, len(params.Severities))
		for i, severity := range params.Severities {
			placeholders[i] = "?"
			args = append(args, strings.ToLower(severity))
		}
		clauses = append(clauses, "LOWER(severity.value) IN ("+strings.Join(placeholders, ",")+")")
	}
	if params.EventType != "" {
		clauses = append(clauses, "event.event_type = ?")
		args = append(args, params.EventType)
	}
	if params.CorrelationKey != "" {
		clauses = append(clauses, "active.correlation_key = ?")
		args = append(args, params.CorrelationKey)
	}
	if params.CorrelationValue != "" {
		clauses = append(clauses, "active.correlation_value = ?")
		args = append(args, params.CorrelationValue)
	}

	return " WHERE " + strings.Join(clauses, " AND "), args
}

// formatTimestamp returns the canonical representation used by SQLite text
// comparisons. Keeping every stored value and query bound in UTC makes
// lexicographic ordering equivalent to chronological ordering.
func formatTimestamp(t time.Time) string {
	return canonicalTime(t).Format(time.RFC3339)
}

func canonicalTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

// nullableString returns a *string pointer for use with SQL parameters.
// It returns nil when s is empty, so the column is stored as NULL.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint failure.
func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
