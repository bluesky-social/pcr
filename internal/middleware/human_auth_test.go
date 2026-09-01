package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/humanauth"
	"github.com/sarahmaeve/go-prod-change-registry/internal/middleware"
)

func TestRequireHumanAuth(t *testing.T) {
	t.Parallel()

	secret := []byte("0123456789abcdef0123456789abcdef")
	principal := humanauth.Principal{Provider: "github", Subject: "12345", UserName: "alice"}
	cookieRecorder := httptest.NewRecorder()
	if err := middleware.SetHumanSessionCookie(cookieRecorder, middleware.HumanSessionOptions{
		Secret: secret, Duration: time.Hour,
	}, principal); err != nil {
		t.Fatalf("SetHumanSessionCookie(): %v", err)
	}
	cookie := cookieRecorder.Result().Cookies()[0]

	var gotSession middleware.HumanSession
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		gotSession, ok = middleware.HumanSessionFromContext(r.Context())
		if !ok {
			t.Error("HumanSessionFromContext() ok = false, want true")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	protected := middleware.RequireHumanAuth(secret, "github")(next)

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/events/one?view=detail", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("protected response status = %d, want 204", recorder.Code)
	}
	if gotSession.Subject != "12345" || gotSession.UserName != "alice" {
		t.Errorf("context session = %+v, want alice/12345", gotSession)
	}
}

func TestRequireHumanAuthRejectsMissingSession(t *testing.T) {
	t.Parallel()

	protected := middleware.RequireHumanAuth([]byte("secret"), "github")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("next handler called without a session")
	}))

	getRecorder := httptest.NewRecorder()
	protected.ServeHTTP(getRecorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/events/one?view=detail", nil))
	if getRecorder.Code != http.StatusFound {
		t.Errorf("GET status = %d, want 302", getRecorder.Code)
	}
	if got := getRecorder.Header().Get("Location"); got != "/login?return_to=%2Fevents%2Fone%3Fview%3Ddetail" {
		t.Errorf("GET Location = %q, want encoded local return path", got)
	}

	postRecorder := httptest.NewRecorder()
	protected.ServeHTTP(postRecorder, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/one/star", nil))
	if postRecorder.Code != http.StatusUnauthorized {
		t.Errorf("POST status = %d, want 401", postRecorder.Code)
	}
}

func TestRequireBoundHumanAuthRefreshesProfileAndRejectsIdentityMismatch(t *testing.T) {
	t.Parallel()

	secret := []byte("0123456789abcdef0123456789abcdef")
	stored := humanauth.Principal{Provider: "beyond", Subject: "alice@example.com", UserName: "alice@example.com", DisplayName: "Old Name"}
	cookieRecorder := httptest.NewRecorder()
	if err := middleware.SetHumanSessionCookie(cookieRecorder, middleware.HumanSessionOptions{
		Secret: secret, Duration: time.Hour,
	}, stored); err != nil {
		t.Fatalf("SetHumanSessionCookie(): %v", err)
	}
	cookie := cookieRecorder.Result().Cookies()[0]

	current := stored
	current.DisplayName = "New Name"
	principalForRequest := func(*http.Request) (humanauth.Principal, error) {
		return current, nil
	}
	var got middleware.HumanSession
	protected := middleware.RequireBoundHumanAuth(secret, "beyond", principalForRequest)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = middleware.HumanSessionFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("bound response status = %d, want 204", recorder.Code)
	}
	if got.Subject != stored.Subject || got.DisplayName != "New Name" {
		t.Errorf("context session principal = %+v, want refreshed display name with matching identity", got.Principal)
	}

	current.Subject = "bob@example.com"
	current.UserName = current.Subject
	rejectedRequest := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/one/star", nil)
	rejectedRequest.AddCookie(cookie)
	rejectedRecorder := httptest.NewRecorder()
	protected.ServeHTTP(rejectedRecorder, rejectedRequest)
	if rejectedRecorder.Code != http.StatusUnauthorized {
		t.Errorf("identity mismatch status = %d, want 401", rejectedRecorder.Code)
	}
}
