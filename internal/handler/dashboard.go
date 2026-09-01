package handler

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sarahmaeve/go-prod-change-registry/internal/middleware"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
	"github.com/sarahmaeve/go-prod-change-registry/web"
)

// DashboardHandler serves the server-rendered HTML dashboard.
type DashboardHandler struct {
	svc           *service.ChangeService
	sessionSecret []byte
	refreshSec    int
	dashboardTmpl *template.Template
	detailTmpl    *template.Template
	recordTmpl    *template.Template
}

// NewDashboardHandler parses the embedded templates and returns a ready handler.
func NewDashboardHandler(svc *service.ChangeService, refreshSec int, sessionSecret []byte) *DashboardHandler {
	funcMap := template.FuncMap{
		"formatTime": func(t time.Time) string {
			return t.UTC().Format("2006-01-02 15:04:05 UTC")
		},
		"formatTags": func(tags map[string]string) []string {
			if len(tags) == 0 {
				return []string{}
			}
			out := make([]string, 0, len(tags))
			for k, v := range tags {
				out = append(out, k+"="+v)
			}
			sort.Strings(out)
			return out
		},
		"tagFilterURL":          tagFilterURL,
		"dashboardTagFilterURL": dashboardTagFilterURL,
		"dashboardTagRemoveURL": dashboardTagRemoveURL,
		"tagValue": func(tags map[string]string, key string) string {
			return tags[key]
		},
		"formatElapsed": formatElapsed,
		"hasValue":      slices.Contains[[]string, string],
		"dashboardViewURL": func(view, team string) string {
			return dashboardURL(view, team, "")
		},
		"dashboardRangeURL": dashboardURL,
		"currentPresetURL":  currentPresetURL,
	}

	// Parse each page template separately with the shared layout
	// to avoid "content" block name collisions.
	dashboardTmpl := template.Must(
		template.New("").Funcs(funcMap).ParseFS(
			web.TemplateFS,
			"templates/layout.html",
			"templates/dashboard.html",
		),
	)
	detailTmpl := template.Must(
		template.New("").Funcs(funcMap).ParseFS(
			web.TemplateFS,
			"templates/layout.html",
			"templates/detail.html",
		),
	)
	recordTmpl := template.Must(
		template.New("").Funcs(funcMap).ParseFS(
			web.TemplateFS,
			"templates/layout.html",
			"templates/record.html",
		),
	)

	return &DashboardHandler{
		svc:           svc,
		sessionSecret: sessionSecret,
		refreshSec:    refreshSec,
		dashboardTmpl: dashboardTmpl,
		detailTmpl:    detailTmpl,
		recordTmpl:    recordTmpl,
	}
}

// dashboardFilters holds the current filter values for re-populating the form.
type dashboardFilters struct {
	View        string
	Team        string
	Range       string
	StartAfter  string
	StartBefore string
	EventType   string
	UserName    string
	Alerted     bool
	Tags        []string
	Scopes      []string
	Severities  []string
}

// dashboardEvent wraps a ChangeEvent with its derived annotation state.
type dashboardEvent struct {
	model.ChangeEvent
	Starred bool
	Alerted bool
}

// dashboardData is the template data for the dashboard page.
type dashboardData struct {
	RefreshSec   int
	UserName     string
	LogoutCSRF   string
	CSRFToken    string
	Events       []dashboardEvent
	BannerEvents []model.ChangeEvent
	BannerTotal  int
	BannerURL    string
	Filters      dashboardFilters
	TotalCount   int
	Limit        int
	Offset       int
	HasPrev      bool
	HasNext      bool
	PrevURL      string
	NextURL      string
	OffsetStart  int
	OffsetEnd    int
}

// detailData is the template data for the detail page.
type detailData struct {
	RefreshSec     int
	UserName       string
	LogoutCSRF     string
	CSRFToken      string
	Event          *model.ChangeEvent
	Annotations    *model.EventAnnotations
	Links          []model.EventLink
	Activity       []model.ChangeEvent
	OperationState string
	ActionError    string
	PendingLinks   []model.EventLink
}

// quickRanges maps the quick-select range values to durations.
var quickRanges = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
}

const dashboardBannerLimit = 20

// Dashboard handles GET / and renders the event list.
func (h *DashboardHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	params, filters := parseDashboardRequest(r)

	result, err := h.listDashboardEvents(r.Context(), params, filters)
	if err != nil {
		slog.ErrorContext(r.Context(), "dashboard list events error", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	bannerParams := highVisibilityParams(filters)
	banner, err := h.svc.ListCurrent(r.Context(), bannerParams)
	if err != nil {
		slog.ErrorContext(r.Context(), "dashboard list high-visibility events error", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	annotations, err := h.fetchAnnotations(r.Context(), result.Events)
	if err != nil {
		slog.ErrorContext(r.Context(), "dashboard fetch annotations error", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	offsetStart := result.Offset + 1
	if result.TotalCount == 0 {
		offsetStart = 0
	}

	data := dashboardData{
		RefreshSec:   h.refreshSec,
		UserName:     humanUserName(r),
		LogoutCSRF:   middleware.GenerateCSRFToken(h.sessionSecret, humanSessionNonce(r)),
		CSRFToken:    middleware.GenerateCSRFToken(h.sessionSecret, humanSessionNonce(r)),
		Events:       buildDashboardEvents(result.Events, annotations),
		BannerEvents: banner.Events,
		BannerTotal:  banner.TotalCount,
		BannerURL:    currentURL(bannerParams),
		Filters:      filters,
		TotalCount:   result.TotalCount,
		Limit:        result.Limit,
		Offset:       result.Offset,
		HasPrev:      result.Offset > 0,
		HasNext:      result.Offset+result.Limit < result.TotalCount,
		PrevURL:      h.paginationURL(r, result.Offset-result.Limit, result.Limit),
		NextURL:      h.paginationURL(r, result.Offset+result.Limit, result.Limit),
		OffsetStart:  offsetStart,
		OffsetEnd:    result.Offset + len(result.Events),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.dashboardTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		slog.ErrorContext(r.Context(), "dashboard template execute error", "error", err)
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

func (h *DashboardHandler) listDashboardEvents(ctx context.Context, params model.ListParams, filters dashboardFilters) (*model.ListResult, error) {
	switch filters.View {
	case "current":
		return h.svc.ListCurrent(ctx, model.CurrentParams{
			ForTeam:    filters.Team,
			Scopes:     filters.Scopes,
			Severities: filters.Severities,
			EventType:  params.EventType,
			Limit:      params.Limit,
			Offset:     params.Offset,
		})
	case "site":
		return h.svc.ListCurrent(ctx, model.CurrentParams{
			Scopes:     []string{"site"},
			Severities: filters.Severities,
			EventType:  params.EventType,
			Limit:      params.Limit,
			Offset:     params.Offset,
		})
	default:
		return h.svc.List(ctx, params)
	}
}

func dashboardURL(view, team, rangeValue string) string {
	q := url.Values{}
	q.Set("view", view)
	if team != "" {
		q.Set("team", team)
	}
	if rangeValue != "" {
		q.Set("range", rangeValue)
	}
	return "/?" + q.Encode()
}

// tagFilterURL builds a context-free dashboard link for tags shown outside
// the dashboard, such as on an event detail page.
func tagFilterURL(key, value string) string {
	q := url.Values{}
	q.Set("tag", key+":"+value)
	return dashboardQueryURL(q)
}

// dashboardFilterQuery reconstructs the dashboard's understood filter state.
// Building this from parsed filters, instead of copying the request query,
// prevents unrelated or sensitive query parameters from being reflected into
// tag links.
func dashboardFilterQuery(filters dashboardFilters) url.Values {
	q := url.Values{}
	if filters.View != "" {
		q.Set("view", filters.View)
	}
	if filters.Team != "" {
		q.Set("team", filters.Team)
	}
	if filters.Range != "" {
		q.Set("range", filters.Range)
	}
	if filters.StartAfter != "" {
		q.Set("start_after", filters.StartAfter)
	}
	if filters.StartBefore != "" {
		q.Set("start_before", filters.StartBefore)
	}
	if filters.EventType != "" {
		q.Set("type", filters.EventType)
	}
	if filters.UserName != "" {
		q.Set("user", filters.UserName)
	}
	for _, scope := range filters.Scopes {
		q.Add("scope", scope)
	}
	for _, severity := range filters.Severities {
		q.Add("severity", severity)
	}
	for _, tag := range filters.Tags {
		q.Add("tag", tag)
	}
	return q
}

// dashboardTagFilterURL adds an exact tag filter without discarding the
// dashboard's other filters. Since ListParams supports one value per tag key,
// selecting a new value replaces an existing value with the same key.
func dashboardTagFilterURL(filters dashboardFilters, key, value string) string {
	q := dashboardFilterQuery(filters)
	q.Del("tag")
	for _, tag := range filters.Tags {
		existingKey, _, ok := strings.Cut(tag, ":")
		if ok && existingKey != key {
			q.Add("tag", tag)
		}
	}
	q.Add("tag", key+":"+value)
	if key == "team" {
		q.Set("team", value)
	}
	return dashboardQueryURL(q)
}

// dashboardTagRemoveURL removes one active tag while preserving the remaining
// dashboard filters. Removing the tag that established team context clears
// that context as well, so the summary does not continue to show that team.
func dashboardTagRemoveURL(filters dashboardFilters, removeTag string) string {
	q := dashboardFilterQuery(filters)
	q.Del("tag")
	for _, tag := range filters.Tags {
		if tag != removeTag {
			q.Add("tag", tag)
		}
	}
	if key, value, ok := strings.Cut(removeTag, ":"); ok && key == "team" && filters.Team == value {
		q.Del("team")
	}
	return dashboardQueryURL(q)
}

func dashboardQueryURL(q url.Values) string {
	if len(q) == 0 {
		return "/"
	}
	return "/?" + q.Encode()
}

func currentURL(params model.CurrentParams) string {
	q := url.Values{"view": {"current"}}
	if params.ForTeam != "" {
		q.Set("team", params.ForTeam)
	}
	for _, scope := range params.Scopes {
		q.Add("scope", scope)
	}
	for _, severity := range params.Severities {
		q.Add("severity", severity)
	}
	if params.EventType != "" {
		q.Set("type", params.EventType)
	}
	return "/?" + q.Encode()
}

func currentPresetURL(preset, team string) string {
	params := model.CurrentParams{ForTeam: team}
	switch preset {
	case "high-severity":
		params.Severities = []string{"sev0", "sev1"}
	case "site":
		params.Scopes = []string{"site"}
	case "maintenance":
		params.EventType = "maintenance"
	}
	return currentURL(params)
}

func highVisibilityParams(filters dashboardFilters) model.CurrentParams {
	scopes := slices.Clone(filters.Scopes)
	if filters.View == "site" {
		scopes = []string{"site"}
	}
	severities := slices.Clone(filters.Severities)
	if len(severities) == 0 {
		severities = []string{"sev0", "sev1"}
	}
	return model.CurrentParams{
		ForTeam:    filters.Team,
		Scopes:     scopes,
		Severities: severities,
		EventType:  filters.EventType,
		Limit:      dashboardBannerLimit,
	}
}

func formatElapsed(timestamp time.Time) string {
	duration := time.Since(timestamp)
	if duration < time.Minute {
		return "<1m"
	}

	duration = duration.Truncate(time.Minute)
	days := int(duration / (24 * time.Hour))
	hours := int(duration % (24 * time.Hour) / time.Hour)
	minutes := int(duration % time.Hour / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// fetchAnnotations resolves annotations for every event in the slice in a
// single batch call. Returns an empty map when there are no events, so
// callers never have to guard the lookup.
func (h *DashboardHandler) fetchAnnotations(ctx context.Context, events []model.ChangeEvent) (map[string]*model.EventAnnotations, error) {
	if len(events) == 0 {
		return map[string]*model.EventAnnotations{}, nil
	}
	ids := make([]string, len(events))
	for i, ev := range events {
		ids[i] = ev.ID
	}
	return h.svc.GetAnnotationsBatch(ctx, ids)
}

// buildDashboardEvents pairs each event with its annotation state. Events
// with no annotations (or a nil entry in the map) are returned with
// Starred/Alerted = false.
func buildDashboardEvents(events []model.ChangeEvent, annotations map[string]*model.EventAnnotations) []dashboardEvent {
	out := make([]dashboardEvent, len(events))
	for i, ev := range events {
		de := dashboardEvent{ChangeEvent: ev}
		if ann, ok := annotations[ev.ID]; ok && ann != nil {
			de.Starred = ann.Starred
			de.Alerted = ann.Alerted
		}
		out[i] = de
	}
	return out
}

// Detail handles GET /events/{id} and renders the event detail page.
func (h *DashboardHandler) Detail(w http.ResponseWriter, r *http.Request) {
	h.renderDetail(w, r, http.StatusOK, "", nil)
}

func (h *DashboardHandler) renderDetail(w http.ResponseWriter, r *http.Request, status int, actionError string, pendingLinks []model.EventLink) {
	id := chi.URLParam(r, "id")

	event, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrEventNotFound) {
			http.Error(w, "Event not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "detail get event error", "error", err, "event_id", id)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	annotations, err := h.svc.GetAnnotations(r.Context(), id)
	if err != nil {
		slog.ErrorContext(r.Context(), "detail get annotations error", "error", err, "event_id", id)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	activity, err := h.svc.GetActivity(r.Context(), id)
	if err != nil {
		slog.ErrorContext(r.Context(), "detail get activity error", "error", err, "event_id", id)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	operationState, err := h.svc.OperationState(r.Context(), event)
	if err != nil {
		slog.ErrorContext(r.Context(), "detail get operation state error", "error", err, "event_id", id)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	links := slices.Clone(event.Links)
	for _, annotation := range activity {
		links = append(links, annotation.Links...)
	}

	data := detailData{
		RefreshSec:     0,
		UserName:       humanUserName(r),
		LogoutCSRF:     middleware.GenerateCSRFToken(h.sessionSecret, humanSessionNonce(r)),
		CSRFToken:      middleware.GenerateCSRFToken(h.sessionSecret, humanSessionNonce(r)),
		Event:          event,
		Annotations:    annotations,
		Links:          links,
		Activity:       activity,
		OperationState: operationState,
		ActionError:    actionError,
		PendingLinks:   pendingLinks,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := h.detailTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		slog.ErrorContext(r.Context(), "detail template execute error", "error", err, "event_id", id)
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

// ToggleStar handles POST /events/{id}/star -- posts a meta-event and redirects back.
func (h *DashboardHandler) ToggleStar(w http.ResponseWriter, r *http.Request) {
	if !h.validateActionForm(w, r) {
		return
	}
	user, ok := humanIdentity(r)
	if !ok {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")

	event, err := h.svc.ToggleStarAs(r.Context(), id, user)
	if err != nil {
		if errors.Is(err, service.ErrEventNotFound) {
			http.Error(w, "Event not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "dashboard toggle star error", "error", err, "event_id", id)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	logHumanAction(r, "toggle_star", user, event.ID)

	redirectAfterAction(w, r)
}

// ToggleAlert appends an alert or clear-alert transition and redirects back.
func (h *DashboardHandler) ToggleAlert(w http.ResponseWriter, r *http.Request) {
	if !h.validateActionForm(w, r) {
		return
	}
	user, ok := humanIdentity(r)
	if !ok {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}
	event, err := h.svc.ToggleAlertAs(r.Context(), chi.URLParam(r, "id"), user)
	if err != nil {
		h.writeActionError(w, r, err, "toggle alert")
		return
	}
	logHumanAction(r, "toggle_alert", user, event.ID)
	redirectAfterAction(w, r)
}

// AddLinks appends one link annotation containing all submitted link rows.
func (h *DashboardHandler) AddLinks(w http.ResponseWriter, r *http.Request) {
	if !h.validateActionForm(w, r) {
		return
	}
	user, ok := humanIdentity(r)
	if !ok {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}
	links := parseLinkForm(r.PostForm)
	event, err := h.svc.AddLinksAs(r.Context(), chi.URLParam(r, "id"), user, links)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrLinksRequired):
			h.renderDetail(w, r, http.StatusBadRequest, "Add at least one link.", links)
			return
		case errors.Is(err, service.ErrInvalidLink):
			h.renderDetail(w, r, http.StatusBadRequest, invalidLinkMessage, links)
			return
		}
		h.writeActionError(w, r, err, "add links")
		return
	}
	logHumanAction(r, "add_links", user, event.ID, "link_count", len(links))
	redirectAfterAction(w, r)
}

// CloseOperation appends a correlated end event and redirects back.
func (h *DashboardHandler) CloseOperation(w http.ResponseWriter, r *http.Request) {
	if !h.validateActionForm(w, r) {
		return
	}
	user, ok := humanIdentity(r)
	if !ok {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}
	event, err := h.svc.CloseOperationAs(
		r.Context(),
		chi.URLParam(r, "id"),
		user,
		r.PostFormValue("description"),
	)
	if err != nil {
		h.writeActionError(w, r, err, "close operation")
		return
	}
	logHumanAction(r, "close_operation", user, event.ID)
	redirectAfterAction(w, r)
}

func logHumanAction(r *http.Request, action string, user model.UserIdentity, eventID string, attrs ...any) {
	fields := []any{
		"action", action,
		"event_id", eventID,
		"parent_event_id", chi.URLParam(r, "id"),
		"provider", user.Provider,
		"subject", user.Subject,
		"user_name", user.Name,
	}
	fields = append(fields, attrs...)
	slog.InfoContext(r.Context(), "human dashboard action succeeded", fields...)
}

func (h *DashboardHandler) validateActionForm(w http.ResponseWriter, r *http.Request) bool {
	if !parseBoundedPostForm(w, r) {
		return false
	}
	if !middleware.ValidateCSRFToken(h.sessionSecret, humanSessionNonce(r), r.PostFormValue("csrf_token")) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func humanSessionNonce(r *http.Request) string {
	session, ok := middleware.HumanSessionFromContext(r.Context())
	if !ok {
		return ""
	}
	return session.Nonce
}

func humanUserName(r *http.Request) string {
	session, ok := middleware.HumanSessionFromContext(r.Context())
	if !ok {
		return ""
	}
	return session.UserName
}

func humanIdentity(r *http.Request) (model.UserIdentity, bool) {
	session, ok := middleware.HumanSessionFromContext(r.Context())
	if !ok || !session.IsValid() {
		return model.UserIdentity{}, false
	}
	return model.UserIdentity{Name: session.UserName, Provider: session.Provider, Subject: session.Subject}, true
}

func (h *DashboardHandler) writeActionError(w http.ResponseWriter, r *http.Request, err error, action string) {
	switch {
	case errors.Is(err, service.ErrEventNotFound):
		http.Error(w, "Event not found", http.StatusNotFound)
	case errors.Is(err, service.ErrLinksRequired), errors.Is(err, service.ErrInvalidLink), errors.Is(err, service.ErrOperationNotClosable):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, service.ErrOperationClosed):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		slog.ErrorContext(r.Context(), "dashboard action error", "action", action, "error", err, "event_id", chi.URLParam(r, "id"))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func redirectAfterAction(w http.ResponseWriter, r *http.Request) {
	// localRedirectTarget rejects scheme-relative and backslash-based paths,
	// including percent-encoded variants after url.Parse decodes them.
	redirect := localRedirectTarget(r.Header.Get("Referer"))
	http.Redirect(w, r, redirect, http.StatusSeeOther) //nolint:gosec // G710: target is reduced to a validated local request URI above.
}

func localRedirectTarget(referer string) string {
	u, err := url.Parse(referer)
	if err != nil || u.Path == "" || !strings.HasPrefix(u.Path, "/") ||
		strings.HasPrefix(u.Path, "//") || strings.Contains(u.Path, "\\") {
		return "/"
	}

	return (&url.URL{Path: u.Path, RawQuery: u.Query().Encode()}).RequestURI()
}

// paginationURL builds a URL preserving current query params but updating offset and limit.
func (h *DashboardHandler) paginationURL(r *http.Request, newOffset, limit int) string {
	if newOffset < 0 {
		newOffset = 0
	}
	q := url.Values{}
	for key, vals := range r.URL.Query() {
		if key == "offset" || key == "limit" {
			continue
		}
		for _, v := range vals {
			q.Add(key, v)
		}
	}
	q.Set("offset", strconv.Itoa(newOffset))
	q.Set("limit", strconv.Itoa(limit))
	return fmt.Sprintf("/?%s", q.Encode())
}
