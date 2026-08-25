package service_test

import (
	"context"
	"testing"

	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/service"
)

func TestCreatePreservesVerifiedIdentity(t *testing.T) {
	t.Parallel()

	var captured *model.ChangeEvent
	store := &mockStore{
		createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			captured = event
			return event, nil
		},
	}
	_, err := service.NewChangeService(store).Create(t.Context(), &model.CreateChangeRequest{
		UserName:     "alice",
		UserProvider: "github",
		UserSubject:  "12345",
		EventType:    model.EventTypeStar,
	})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	if captured == nil {
		t.Fatal("Create() did not call store")
	}
	if captured.UserProvider != "github" || captured.UserSubject != "12345" {
		t.Errorf("Create() identity = %q/%q, want github/12345", captured.UserProvider, captured.UserSubject)
	}
}

func TestDashboardOperationsPreserveVerifiedIdentity(t *testing.T) {
	t.Parallel()

	identity := model.UserIdentity{Name: "alice", Provider: "github", Subject: "12345"}
	var created *model.ChangeEvent
	store := &mockStore{
		createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			created = event
			return event, nil
		},
		getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{ID: "event-one"}, nil
		},
	}
	svc := service.NewChangeService(store)
	_, err := svc.AddLinksAs(t.Context(), "event-one", identity, []model.EventLink{{URL: "https://example.com/runbook"}})
	if err != nil {
		t.Fatalf("AddLinksAs(): %v", err)
	}
	if created.UserName != "alice" || created.UserProvider != "github" || created.UserSubject != "12345" {
		t.Errorf("AddLinksAs() identity = %q/%q/%q, want alice/github/12345", created.UserName, created.UserProvider, created.UserSubject)
	}
}

func TestToggleStarAsPassesVerifiedIdentity(t *testing.T) {
	t.Parallel()

	want := model.UserIdentity{Name: "alice", Provider: "github", Subject: "12345"}
	var got model.UserIdentity
	store := &mockStore{
		toggleStarIdentityFn: func(_ context.Context, _ string, user model.UserIdentity) (*model.ChangeEvent, error) {
			got = user
			return &model.ChangeEvent{}, nil
		},
	}
	if _, err := service.NewChangeService(store).ToggleStarAs(t.Context(), "event-one", want); err != nil {
		t.Fatalf("ToggleStarAs(): %v", err)
	}
	if got != want {
		t.Errorf("ToggleStarAs() identity = %+v, want %+v", got, want)
	}
}
