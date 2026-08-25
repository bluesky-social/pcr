package fixture_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/fixture"
	"github.com/sarah/go-prod-change-registry/internal/model"
)

type recordingCreator struct {
	requests []model.CreateChangeRequest
}

func TestApplyAtResolvesTimestampOffsets(t *testing.T) {
	t.Parallel()

	creator := &recordingCreator{}
	base := time.Date(2026, 8, 24, 20, 0, 0, 0, time.UTC)
	_, err := fixture.ApplyAt(t.Context(), creator, []fixture.Event{{
		Ref:             "relative",
		ExternalID:      "relative",
		TimestampOffset: "-2h30m",
	}}, base)
	if err != nil {
		t.Fatalf("ApplyAt() error = %v", err)
	}
	want := base.Add(-2*time.Hour - 30*time.Minute)
	if got := creator.requests[0].Timestamp; got == nil || !got.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", got, want)
	}
}

func (c *recordingCreator) Create(_ context.Context, request *model.CreateChangeRequest) (*model.ChangeEvent, error) {
	c.requests = append(c.requests, *request)
	return &model.ChangeEvent{ID: "id-" + request.ExternalID}, nil
}

func TestLoadAndApply(t *testing.T) {
	t.Parallel()

	events, err := fixture.Load(strings.NewReader(`{
		"events": [
			{
				"ref": "parent",
				"external_id": "parent-event",
				"user_name": "alice",
				"event_type": "deployment",
				"description": "rollout started",
				"timestamp": "2026-08-24T12:00:00Z",
				"links": [
					{"label": "Pull request", "url": "https://github.com/example/repo/pull/7"},
					{"label": "Incident", "url": "https://example.pagerduty.com/incidents/P123"}
				],
				"tags": {"phase": "start", "change_id": "rollout-1"}
			},
			{
				"ref": "annotation",
				"parent_ref": "parent",
				"external_id": "parent-alert",
				"user_name": "on-call",
				"event_type": "alert",
				"description": "response underway"
			}
		]
	}`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	creator := &recordingCreator{}
	refs, err := fixture.Apply(t.Context(), creator, events)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(creator.requests) != 2 {
		t.Fatalf("created requests = %d, want 2", len(creator.requests))
	}
	if creator.requests[0].Timestamp == nil || creator.requests[0].Timestamp.Format("2006-01-02T15:04:05Z07:00") != "2026-08-24T12:00:00Z" {
		t.Errorf("first timestamp = %v", creator.requests[0].Timestamp)
	}
	if creator.requests[0].Tags["change_id"] != "rollout-1" {
		t.Errorf("first tags = %v", creator.requests[0].Tags)
	}
	if got := creator.requests[0].Links; len(got) != 2 || got[0].Label != "Pull request" || got[1].URL != "https://example.pagerduty.com/incidents/P123" {
		t.Errorf("first links = %#v", got)
	}
	if creator.requests[1].ParentID != refs["parent"] || creator.requests[1].ParentID != "id-parent-event" {
		t.Errorf("annotation ParentID = %q, refs = %v", creator.requests[1].ParentID, refs)
	}
	if refs["annotation"] != "id-parent-alert" {
		t.Errorf("annotation ref = %q", refs["annotation"])
	}
}

func TestApplyRejectsInvalidReferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		events []fixture.Event
		want   string
	}{
		{
			name:   "empty ref",
			events: []fixture.Event{{ExternalID: "event"}},
			want:   "event 1 has empty ref",
		},
		{
			name: "duplicate ref",
			events: []fixture.Event{
				{Ref: "same", ExternalID: "first"},
				{Ref: "same", ExternalID: "second"},
			},
			want: `event 2 repeats ref "same"`,
		},
		{
			name:   "unknown parent",
			events: []fixture.Event{{Ref: "child", ParentRef: "missing", ExternalID: "child"}},
			want:   `event 1 references unknown parent_ref "missing"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := fixture.Apply(t.Context(), &recordingCreator{}, test.events)
			if err == nil || err.Error() != test.want {
				t.Errorf("Apply() error = %v, want %q", err, test.want)
			}
		})
	}
}
