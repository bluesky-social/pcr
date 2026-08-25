package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/humanauth"
	"github.com/sarah/go-prod-change-registry/internal/middleware"
)

func TestHumanSessionRoundTrip(t *testing.T) {
	t.Parallel()

	principal := humanauth.Principal{
		Provider:         "github",
		Subject:          "12345",
		UserName:         "alice",
		DisplayName:      "Alice Example",
		ProfileCheckedAt: time.Now().UTC().Truncate(time.Second),
		AuthorizedVia:    "github_org:example-inc",
	}
	opts := middleware.HumanSessionOptions{
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
		Secure:   true,
		Duration: 12 * time.Hour,
	}
	recorder := httptest.NewRecorder()
	if err := middleware.SetHumanSessionCookie(recorder, opts, principal); err != nil {
		t.Fatalf("SetHumanSessionCookie(): %v", err)
	}

	result := recorder.Result()
	t.Cleanup(func() { _ = result.Body.Close() })
	cookies := result.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("SetHumanSessionCookie() cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie flags = HttpOnly:%v Secure:%v SameSite:%v, want true true Lax", cookie.HttpOnly, cookie.Secure, cookie.SameSite)
	}
	if cookie.MaxAge != int((12*time.Hour)/time.Second) {
		t.Errorf("session cookie MaxAge = %d, want %d", cookie.MaxAge, int((12*time.Hour)/time.Second))
	}

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	session, err := middleware.ReadHumanSession(request, opts.Secret, "github")
	if err != nil {
		t.Fatalf("ReadHumanSession(): %v", err)
	}
	if session.Provider != principal.Provider || session.Subject != principal.Subject || session.UserName != principal.UserName {
		t.Errorf("ReadHumanSession() principal = %+v, want %+v", session.Principal, principal)
	}
	if session.AuthorizedVia != principal.AuthorizedVia {
		t.Errorf("ReadHumanSession() AuthorizedVia = %q, want %q", session.AuthorizedVia, principal.AuthorizedVia)
	}
	if session.Nonce == "" {
		t.Error("ReadHumanSession() Nonce is empty")
	}
	if !session.ExpiresAt.After(session.IssuedAt) || session.ExpiresAt.Sub(session.IssuedAt) != 12*time.Hour {
		t.Errorf("ReadHumanSession() lifetime = %v, want 12h", session.ExpiresAt.Sub(session.IssuedAt))
	}
}

func TestHumanSessionRejectsInvalidCookie(t *testing.T) {
	t.Parallel()

	secret := []byte("0123456789abcdef0123456789abcdef")
	principal := humanauth.Principal{Provider: "google", Subject: "9876", UserName: "alice@example.com"}
	recorder := httptest.NewRecorder()
	err := middleware.SetHumanSessionCookie(recorder, middleware.HumanSessionOptions{
		Secret:   secret,
		Duration: time.Hour,
	}, principal)
	if err != nil {
		t.Fatalf("SetHumanSessionCookie(): %v", err)
	}
	cookie := recorder.Result().Cookies()[0]

	tests := []struct {
		name     string
		provider string
		mutate   func(*http.Cookie)
	}{
		{name: "wrong provider", provider: "github", mutate: func(*http.Cookie) {}},
		{name: "tampered payload", provider: "google", mutate: func(cookie *http.Cookie) { cookie.Value += "x" }}, //nolint:gosec // Mutates an already-secure test cookie.
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			candidate := *cookie
			tc.mutate(&candidate)
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			request.AddCookie(&candidate)
			if _, err := middleware.ReadHumanSession(request, secret, tc.provider); err == nil {
				t.Error("ReadHumanSession() error = nil, want invalid session error")
			}
		})
	}
}

func TestSetHumanSessionCookieRejectsIncompletePrincipal(t *testing.T) {
	t.Parallel()

	err := middleware.SetHumanSessionCookie(httptest.NewRecorder(), middleware.HumanSessionOptions{
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
		Duration: time.Hour,
	}, humanauth.Principal{Provider: "github", UserName: "alice"})
	if err == nil {
		t.Error("SetHumanSessionCookie() error = nil, want incomplete principal error")
	}
}
