package handler_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/handler"
	"github.com/sarahmaeve/go-prod-change-registry/internal/humanauth"
	"github.com/sarahmaeve/go-prod-change-registry/internal/middleware"
)

type fakeHumanAuthenticator struct {
	name          string
	authURL       string
	authenticated humanauth.Principal
	authErr       error
	code          string
	codeVerifier  string
	nonce         string
}

func (a *fakeHumanAuthenticator) Name() string { return a.name }

func (a *fakeHumanAuthenticator) IdentityProvider() string { return a.name }

func (a *fakeHumanAuthenticator) AuthorizationURL(state, codeChallenge, nonce string) string {
	values := url.Values{"state": {state}, "challenge": {codeChallenge}, "nonce": {nonce}}
	return a.authURL + "?" + values.Encode()
}

func (a *fakeHumanAuthenticator) Authenticate(_ context.Context, code, codeVerifier, nonce string) (humanauth.Principal, error) {
	a.code = code
	a.codeVerifier = codeVerifier
	a.nonce = nonce
	return a.authenticated, a.authErr
}

func TestHumanAuthLoginAndCallback(t *testing.T) {
	t.Parallel()

	secret := []byte("0123456789abcdef0123456789abcdef")
	authenticator := &fakeHumanAuthenticator{
		name:    "github",
		authURL: "https://github.example/authorize",
		authenticated: humanauth.Principal{
			Provider:      "github",
			Subject:       "12345",
			UserName:      "alice",
			AuthorizedVia: "github_org:example-inc",
		},
	}
	h := handler.NewHumanAuthHandler(authenticator, handler.HumanAuthOptions{
		SessionSecret:   secret,
		CookieSecure:    true,
		SessionDuration: 12 * time.Hour,
	})

	loginRecorder := httptest.NewRecorder()
	h.ShowLogin(loginRecorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/login", nil))
	if loginRecorder.Code != http.StatusOK || !strings.Contains(loginRecorder.Body.String(), "Sign in with GitHub") {
		t.Errorf("ShowLogin() = %d %q, want GitHub login button", loginRecorder.Code, loginRecorder.Body.String())
	}
	if strings.Contains(loginRecorder.Body.String(), "API Token") || strings.Contains(loginRecorder.Body.String(), "Google") {
		t.Errorf("ShowLogin() unexpectedly offers another auth method: %q", loginRecorder.Body.String())
	}

	startRecorder := httptest.NewRecorder()
	startRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/start?return_to=%2Fevents%2Fone", nil)
	h.Start(startRecorder, startRequest)
	if startRecorder.Code != http.StatusFound {
		t.Fatalf("Start() status = %d, want 302", startRecorder.Code)
	}
	location, err := url.Parse(startRecorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("url.Parse(Location): %v", err)
	}
	state := location.Query().Get("state")
	if state == "" || location.Query().Get("challenge") == "" || location.Query().Get("nonce") == "" {
		t.Errorf("Start() Location = %q, want state, PKCE challenge, and nonce", location.String())
	}
	flowCookies := startRecorder.Result().Cookies()
	if len(flowCookies) != 1 || !flowCookies[0].HttpOnly || !flowCookies[0].Secure {
		t.Fatalf("Start() flow cookies = %#v, want one secure HttpOnly cookie", flowCookies)
	}

	callbackRecorder := httptest.NewRecorder()
	callbackRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/callback?code=provider-code&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(flowCookies[0])
	h.Callback(callbackRecorder, callbackRequest)
	if callbackRecorder.Code != http.StatusSeeOther {
		t.Fatalf("Callback() status = %d, want 303; body = %q", callbackRecorder.Code, callbackRecorder.Body.String())
	}
	if got := callbackRecorder.Header().Get("Location"); got != "/events/one" {
		t.Errorf("Callback() Location = %q, want /events/one", got)
	}
	if authenticator.code != "provider-code" || authenticator.codeVerifier == "" || authenticator.nonce == "" {
		t.Errorf("Authenticate() inputs = code:%q verifier:%q nonce:%q", authenticator.code, authenticator.codeVerifier, authenticator.nonce)
	}

	var sessionCookie *http.Cookie
	for _, cookie := range callbackRecorder.Result().Cookies() {
		if cookie.Name == middleware.SessionCookieName && cookie.MaxAge > 0 {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("Callback() did not set a human session cookie")
	}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	request.AddCookie(sessionCookie)
	session, err := middleware.ReadHumanSession(request, secret, "github")
	if err != nil {
		t.Fatalf("ReadHumanSession(): %v", err)
	}
	if session.Subject != "12345" || session.UserName != "alice" {
		t.Errorf("session identity = %q/%q, want 12345/alice", session.Subject, session.UserName)
	}
}

func TestHumanAuthCallbackRejectsStateMismatch(t *testing.T) {
	t.Parallel()

	authenticator := &fakeHumanAuthenticator{name: "google", authURL: "https://google.example/authorize"}
	h := handler.NewHumanAuthHandler(authenticator, handler.HumanAuthOptions{
		SessionSecret:   []byte("0123456789abcdef0123456789abcdef"),
		SessionDuration: time.Hour,
	})
	startRecorder := httptest.NewRecorder()
	h.Start(startRecorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/start", nil))

	callbackRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/callback?code=code&state=wrong", nil)
	callbackRequest.AddCookie(startRecorder.Result().Cookies()[0])
	callbackRecorder := httptest.NewRecorder()
	h.Callback(callbackRecorder, callbackRequest)
	if callbackRecorder.Code != http.StatusBadRequest {
		t.Errorf("Callback() status = %d, want 400", callbackRecorder.Code)
	}
	if authenticator.code != "" {
		t.Error("Callback() called provider after state mismatch")
	}
}

func TestBeyondHumanAuthStartsFromTrustedHeaders(t *testing.T) {
	t.Parallel()

	secret := []byte("0123456789abcdef0123456789abcdef")
	provider := humanauth.NewBeyond(humanauth.ProviderOptions{AllowAny: true})
	h := handler.NewBeyondHumanAuthHandler(provider, handler.HumanAuthOptions{
		SessionSecret: secret, CookieSecure: true, SessionDuration: time.Hour,
	})

	loginRecorder := httptest.NewRecorder()
	h.ShowLogin(loginRecorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/login", nil))
	if loginRecorder.Code != http.StatusOK || !strings.Contains(loginRecorder.Body.String(), "Continue with Company SSO") {
		t.Errorf("ShowLogin() = %d %q, want company SSO button", loginRecorder.Code, loginRecorder.Body.String())
	}

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/start?return_to=%2Fevents%2Fone", nil)
	request.Header.Set("X-Beyond-Email", "alice@example.com")
	request.Header.Set("X-Beyond-Name", "Alice Example")
	recorder := httptest.NewRecorder()
	h.Start(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/events/one" {
		t.Fatalf("Start() = %d Location %q, want 303 /events/one", recorder.Code, recorder.Header().Get("Location"))
	}

	var sessionCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == middleware.SessionCookieName && cookie.MaxAge > 0 {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("Start() did not set a human session cookie")
	}
	readRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	readRequest.AddCookie(sessionCookie)
	session, err := middleware.ReadHumanSession(readRequest, secret, "beyond")
	if err != nil {
		t.Fatalf("ReadHumanSession(): %v", err)
	}
	if session.Subject != "alice@example.com" || session.UserName != "alice@example.com" {
		t.Errorf("session identity = %q/%q, want alice@example.com", session.Subject, session.UserName)
	}

	callbackRecorder := httptest.NewRecorder()
	h.Callback(callbackRecorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/callback", nil))
	if callbackRecorder.Code != http.StatusNotFound {
		t.Errorf("Callback() status = %d, want 404 in Beyond mode", callbackRecorder.Code)
	}
}

func TestHumanAuthCallbackExplainsOrganizationVerificationFailure(t *testing.T) {
	t.Parallel()

	authenticator := &fakeHumanAuthenticator{
		name:    "github",
		authURL: "https://github.example/authorize",
		authErr: fmt.Errorf("%w: %w: GitHub organization membership returned HTTP 403", humanauth.ErrProviderUnavailable, humanauth.ErrOrganizationVerification),
	}
	h := handler.NewHumanAuthHandler(authenticator, handler.HumanAuthOptions{
		SessionSecret:   []byte("0123456789abcdef0123456789abcdef"),
		SessionDuration: time.Hour,
	})
	startRecorder := httptest.NewRecorder()
	h.Start(startRecorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/start", nil))
	location, err := url.Parse(startRecorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("url.Parse(Location): %v", err)
	}

	callbackRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/callback?code=code&state="+url.QueryEscape(location.Query().Get("state")), nil)
	callbackRequest.AddCookie(startRecorder.Result().Cookies()[0])
	callbackRecorder := httptest.NewRecorder()
	h.Callback(callbackRecorder, callbackRequest)

	if callbackRecorder.Code != http.StatusServiceUnavailable {
		t.Errorf("Callback() status = %d, want 503", callbackRecorder.Code)
	}
	if body := callbackRecorder.Body.String(); !strings.Contains(body, "could not verify your GitHub organization membership") {
		t.Errorf("Callback() body = %q, want organization-verification guidance", body)
	}
}

func TestHumanAuthLogoutRequiresSessionCSRF(t *testing.T) {
	t.Parallel()

	secret := []byte("0123456789abcdef0123456789abcdef")
	h := handler.NewHumanAuthHandler(&fakeHumanAuthenticator{name: "github"}, handler.HumanAuthOptions{
		SessionSecret: secret, SessionDuration: time.Hour,
	})
	cookieRecorder := httptest.NewRecorder()
	if err := middleware.SetHumanSessionCookie(cookieRecorder, middleware.HumanSessionOptions{
		Secret: secret, Duration: time.Hour,
	}, humanauth.Principal{Provider: "github", Subject: "12345", UserName: "alice"}); err != nil {
		t.Fatalf("SetHumanSessionCookie(): %v", err)
	}
	cookie := cookieRecorder.Result().Cookies()[0]
	readRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	readRequest.AddCookie(cookie)
	session, err := middleware.ReadHumanSession(readRequest, secret, "github")
	if err != nil {
		t.Fatalf("ReadHumanSession(): %v", err)
	}
	form := url.Values{"csrf_token": {middleware.GenerateCSRFToken(secret, session.Nonce)}}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	middleware.RequireHumanAuth(secret, "github")(http.HandlerFunc(h.Logout)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
		t.Errorf("Logout() = %d Location %q, want 303 /login", recorder.Code, recorder.Header().Get("Location"))
	}
	cleared := false
	for _, resultCookie := range recorder.Result().Cookies() {
		if resultCookie.Name == middleware.SessionCookieName && resultCookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("Logout() did not clear session cookie")
	}
}
