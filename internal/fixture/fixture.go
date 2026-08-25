// Package fixture loads and applies deterministic event fixtures.
package fixture

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

// Event is one fixture record. Ref names the created event for later ParentRef lookups.
type Event struct {
	Ref             string            `json:"ref"`
	ParentRef       string            `json:"parent_ref,omitempty"`
	ExternalID      string            `json:"external_id,omitempty"`
	UserName        string            `json:"user_name"`
	Timestamp       *time.Time        `json:"timestamp,omitempty"`
	TimestampOffset string            `json:"timestamp_offset,omitempty"`
	EventType       string            `json:"event_type"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description,omitempty"`
	Links           []model.EventLink `json:"links,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
}

type document struct {
	Events []Event `json:"events"`
}

// Creator is the event-creation capability required to apply a fixture.
type Creator interface {
	Create(ctx context.Context, request *model.CreateChangeRequest) (*model.ChangeEvent, error)
}

// Load decodes a fixture document and rejects unknown or trailing JSON.
func Load(reader io.Reader) ([]Event, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()

	var doc document
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode fixture: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode fixture: multiple JSON values")
		}
		return nil, fmt.Errorf("decode fixture trailing data: %w", err)
	}
	return doc.Events, nil
}

// Apply creates fixture events in order and returns their IDs keyed by Ref.
// A ParentRef must name an earlier fixture event.
func Apply(ctx context.Context, creator Creator, events []Event) (map[string]string, error) {
	return ApplyAt(ctx, creator, events, time.Now().UTC())
}

// ApplyAt creates fixture events relative to baseTime and returns their IDs keyed by Ref.
// A ParentRef must name an earlier fixture event.
func ApplyAt(ctx context.Context, creator Creator, events []Event, baseTime time.Time) (map[string]string, error) {
	if err := validateReferences(events); err != nil {
		return nil, err
	}

	refs := make(map[string]string, len(events))
	for i, event := range events {
		timestamp := event.Timestamp
		if event.TimestampOffset != "" {
			offset, _ := time.ParseDuration(event.TimestampOffset)
			resolved := baseTime.Add(offset)
			timestamp = &resolved
		}
		request := model.CreateChangeRequest{
			ExternalID:      event.ExternalID,
			UserName:        event.UserName,
			Timestamp:       timestamp,
			EventType:       event.EventType,
			Description:     event.Description,
			LongDescription: event.LongDescription,
			Links:           event.Links,
			Tags:            event.Tags,
		}
		if event.ParentRef != "" {
			request.ParentID = refs[event.ParentRef]
		}

		created, err := creator.Create(ctx, &request)
		if err != nil {
			return nil, fmt.Errorf("create fixture event %d (%q): %w", i+1, event.Ref, err)
		}
		refs[event.Ref] = created.ID
	}
	return refs, nil
}

func validateReferences(events []Event) error {
	seen := make(map[string]struct{}, len(events))
	for i, event := range events {
		if err := validateEvent(i+1, event, seen); err != nil {
			return err
		}
		seen[event.Ref] = struct{}{}
	}
	return nil
}

func validateEvent(position int, event Event, seen map[string]struct{}) error {
	if event.Ref == "" {
		return fmt.Errorf("event %d has empty ref", position)
	}
	if _, ok := seen[event.Ref]; ok {
		return fmt.Errorf("event %d repeats ref %q", position, event.Ref)
	}
	if event.ParentRef != "" {
		if _, ok := seen[event.ParentRef]; !ok {
			return fmt.Errorf("event %d references unknown parent_ref %q", position, event.ParentRef)
		}
	}
	if event.Timestamp != nil && event.TimestampOffset != "" {
		return fmt.Errorf("event %d sets both timestamp and timestamp_offset", position)
	}
	if event.TimestampOffset == "" {
		return nil
	}
	if _, err := time.ParseDuration(event.TimestampOffset); err != nil {
		return fmt.Errorf("event %d has invalid timestamp_offset %q: %w", position, event.TimestampOffset, err)
	}
	return nil
}
