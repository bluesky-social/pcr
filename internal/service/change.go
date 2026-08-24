package service

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/store"
)

var (
	ErrUserNameRequired  = errors.New("user_name is required")
	ErrEventTypeRequired = errors.New("event_type is required")
	ErrInvalidLink       = errors.New("links must use absolute http or https URLs")
	ErrEventNotFound     = errors.New("event not found")
	ErrParentNotFound    = errors.New("parent event not found")
)

type ChangeService struct {
	store store.ChangeStore
}

func NewChangeService(store store.ChangeStore) *ChangeService {
	return &ChangeService{store: store}
}

func (s *ChangeService) Create(ctx context.Context, req *model.CreateChangeRequest) (*model.ChangeEvent, error) {
	if req.UserName == "" {
		return nil, ErrUserNameRequired
	}
	if req.EventType == "" {
		return nil, ErrEventTypeRequired
	}
	for _, link := range req.Links {
		parsed, err := url.Parse(link.URL)
		if err != nil || link.URL != strings.TrimSpace(link.URL) || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, ErrInvalidLink
		}
	}

	// If this is a meta-event, verify the parent exists.
	if req.ParentID != "" {
		parent, err := s.store.GetByID(ctx, req.ParentID)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			return nil, ErrParentNotFound
		}
	}

	now := time.Now().UTC()
	ts := now
	if req.Timestamp != nil {
		ts = req.Timestamp.UTC()
	}

	tags := req.Tags
	if tags == nil {
		tags = make(map[string]string)
	}

	event := &model.ChangeEvent{
		ID:              uuid.Must(uuid.NewV7()).String(),
		ExternalID:      req.ExternalID,
		ParentID:        req.ParentID,
		UserName:        req.UserName,
		Timestamp:       ts,
		EventType:       req.EventType,
		Description:     req.Description,
		LongDescription: req.LongDescription,
		Links:           slices.Clone(req.Links),
		Tags:            tags,
		CreatedAt:       now,
	}

	created, err := s.store.Create(ctx, event)
	if errors.Is(err, store.ErrDuplicate) {
		return created, store.ErrDuplicate
	}
	return created, err
}

func (s *ChangeService) GetByID(ctx context.Context, id string) (*model.ChangeEvent, error) {
	event, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if event == nil {
		return nil, ErrEventNotFound
	}
	return event, nil
}

func (s *ChangeService) List(ctx context.Context, params model.ListParams) (*model.ListResult, error) {
	params.Limit = params.EffectiveLimit()
	return s.store.List(ctx, params)
}

// ListCurrent returns active logical operations derived from immutable phase events.
func (s *ChangeService) ListCurrent(ctx context.Context, params model.CurrentParams) (*model.ListResult, error) {
	params.Limit = params.EffectiveLimit()
	return s.store.ListCurrent(ctx, params)
}

func (s *ChangeService) GetAnnotations(ctx context.Context, eventID string) (*model.EventAnnotations, error) {
	return s.store.GetAnnotations(ctx, eventID)
}

func (s *ChangeService) GetAnnotationsBatch(ctx context.Context, eventIDs []string) (map[string]*model.EventAnnotations, error) {
	return s.store.GetAnnotationsBatch(ctx, eventIDs)
}

// ToggleStar creates a star or unstar meta-event for the given event.
func (s *ChangeService) ToggleStar(ctx context.Context, eventID, userName string) (*model.ChangeEvent, error) {
	event, err := s.store.ToggleStar(ctx, eventID, userName)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrEventNotFound
	}
	return event, err
}
