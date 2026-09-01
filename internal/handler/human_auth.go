package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/humanauth"
	"github.com/sarahmaeve/go-prod-change-registry/internal/middleware"
	"github.com/sarahmaeve/go-prod-change-registry/web"
)

const (
	flowCookieName = "pcr_oauth_flow"
	flowLifetime   = 10 * time.Minute
)

// HumanAuthOptions configures the browser login flow and application session.
type HumanAuthOptions struct {
	SessionSecret   []byte
	CookieSecure    bool
	SessionDuration time.Duration
}

// HumanAuthHandler establishes human sessions with the configured provider.
type HumanAuthHandler struct {
	authenticator humanauth.Authenticator
	beyond        *humanauth.Beyond
	opts          HumanAuthOptions
	loginTmpl     *template.Template
}

// NewBeyondHumanAuthHandler creates a handler that establishes local sessions
// from identity headers supplied by a trusted Beyond proxy.
func NewBeyondHumanAuthHandler(beyond *humanauth.Beyond, opts HumanAuthOptions) *HumanAuthHandler {
	loginTmpl := template.Must(template.New("").ParseFS(
		web.TemplateFS,
		"templates/layout.html",
		"templates/human_login.html",
	))
	return &HumanAuthHandler{beyond: beyond, opts: opts, loginTmpl: loginTmpl}
}

// NewHumanAuthHandler creates a browser authentication handler.
func NewHumanAuthHandler(authenticator humanauth.Authenticator, opts HumanAuthOptions) *HumanAuthHandler {
	loginTmpl := template.Must(template.New("").ParseFS(
		web.TemplateFS,
		"templates/layout.html",
		"templates/human_login.html",
	))
	return &HumanAuthHandler{authenticator: authenticator, opts: opts, loginTmpl: loginTmpl}
}

// IdentityProvider returns the durable provider namespace accepted in sessions.
func (h *HumanAuthHandler) IdentityProvider() string {
	if h.beyond != nil {
		return h.beyond.IdentityProvider()
	}
	return h.authenticator.IdentityProvider()
}

// TrustedRequestPrincipal returns the request identity resolver used in
// Beyond mode. OAuth providers return nil because their local session is the
// sole identity source after login.
func (h *HumanAuthHandler) TrustedRequestPrincipal() middleware.RequestPrincipal {
	if h.beyond == nil {
		return nil
	}
	return h.beyond.AuthenticateRequest
}

type humanLoginData struct {
	RefreshSec    int
	ProviderLabel string
	ButtonLabel   string
	ReturnTo      string
	UserName      string
	LogoutCSRF    string
}

// ShowLogin renders the single configured provider choice.
func (h *HumanAuthHandler) ShowLogin(w http.ResponseWriter, r *http.Request) {
	providerLabel := ""
	providerName := "beyond"
	if h.authenticator != nil {
		providerName = h.authenticator.Name()
	}
	switch providerName {
	case "github":
		providerLabel = "GitHub"
	case "google":
		providerLabel = "Google"
	case "authentik":
		providerLabel = "Authentik"
	case "beyond":
		providerLabel = "Company SSO"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buttonLabel := "Sign in with " + providerLabel
	if providerName == "beyond" {
		buttonLabel = "Continue with Company SSO"
	}
	data := humanLoginData{ProviderLabel: providerLabel, ButtonLabel: buttonLabel, ReturnTo: localRedirectTarget(r.URL.Query().Get("return_to"))}
	if err := h.loginTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		slog.ErrorContext(r.Context(), "human login template execute error", "error", err)
	}
}

// Start creates stateless flow data and redirects to the selected provider.
func (h *HumanAuthHandler) Start(w http.ResponseWriter, r *http.Request) {
	if h.beyond != nil {
		h.startBeyond(w, r)
		return
	}
	state, err := randomURLToken(32)
	if err != nil {
		h.writeInternalError(w, r, "generate OAuth state", err)
		return
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		h.writeInternalError(w, r, "generate PKCE verifier", err)
		return
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		h.writeInternalError(w, r, "generate OIDC nonce", err)
		return
	}
	now := time.Now().UTC()
	flow := oauthFlow{
		State:     state,
		Verifier:  verifier,
		Nonce:     nonce,
		ReturnTo:  localRedirectTarget(r.URL.Query().Get("return_to")),
		IssuedAt:  now,
		ExpiresAt: now.Add(flowLifetime),
		Provider:  h.authenticator.Name(),
	}
	value, err := encodeOAuthFlow(h.opts.SessionSecret, flow)
	if err != nil {
		h.writeInternalError(w, r, "encode OAuth flow", err)
		return
	}
	h.setFlowCookie(w, value, flow.ExpiresAt, int(flowLifetime/time.Second))
	challenge := sha256.Sum256([]byte(verifier))
	authorizationURL := h.authenticator.AuthorizationURL(state, base64.RawURLEncoding.EncodeToString(challenge[:]), nonce)
	http.Redirect(w, r, authorizationURL, http.StatusFound)
}

// Callback verifies one-time flow state, authenticates the user, and creates a PCR session.
func (h *HumanAuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	if h.beyond != nil {
		http.NotFound(w, r)
		return
	}
	h.clearFlowCookie(w)
	if r.URL.Query().Get("error") != "" {
		http.Error(w, "Authentication was not completed", http.StatusBadRequest)
		return
	}
	flow, err := h.readOAuthFlow(r)
	if err != nil || !hmac.Equal([]byte(flow.State), []byte(r.URL.Query().Get("state"))) {
		http.Error(w, "Invalid or expired authentication flow", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Missing authorization code", http.StatusBadRequest)
		return
	}
	principal, err := h.authenticator.Authenticate(r.Context(), code, flow.Verifier, flow.Nonce)
	if err != nil {
		switch {
		case errors.Is(err, humanauth.ErrOrganizationVerification):
			slog.WarnContext(r.Context(), "human organization membership verification failed",
				"provider", h.authenticator.Name(),
				"error", err,
			)
			http.Error(w, "PCR could not verify your GitHub organization membership. The OAuth application may require organization approval or SSO authorization; retry if GitHub is temporarily unavailable.", http.StatusServiceUnavailable)
		case errors.Is(err, humanauth.ErrGroupUnauthorized):
			slog.WarnContext(r.Context(), "human authentication group denied", "provider", h.authenticator.Name())
			http.Error(w, "This account is not in an allowed Authentik group. Check PCR_ALLOWED_ORGS and the Authentik groups scope mapping.", http.StatusForbidden)
		case errors.Is(err, humanauth.ErrUnauthorized):
			slog.WarnContext(r.Context(), "human authentication denied", "provider", h.authenticator.Name())
			http.Error(w, "This account is not authorized", http.StatusForbidden)
		case errors.Is(err, humanauth.ErrProviderUnavailable):
			// ErrProviderUnavailable contains only the failed provider operation
			// and status; provider response bodies and OAuth material are excluded.
			slog.WarnContext(r.Context(), "human authentication provider unavailable",
				"provider", h.authenticator.Name(),
				"error", err,
			)
			http.Error(w, "Identity provider is temporarily unavailable", http.StatusServiceUnavailable)
		default:
			// Provider errors can contain callback material. Keep the audit log to
			// a safe category instead of recording the error or OAuth response.
			slog.WarnContext(r.Context(), "human authentication callback failed", "provider", h.authenticator.Name())
			http.Error(w, "Authentication failed", http.StatusBadGateway)
		}
		return
	}
	err = middleware.SetHumanSessionCookie(w, middleware.HumanSessionOptions{
		Secret:   h.opts.SessionSecret,
		Secure:   h.opts.CookieSecure,
		Duration: h.opts.SessionDuration,
	}, principal)
	if err != nil {
		h.writeInternalError(w, r, "create human session", err)
		return
	}
	slog.InfoContext(r.Context(), "human authentication succeeded",
		"provider", principal.Provider,
		"subject", principal.Subject,
		"user_name", principal.UserName,
		"authorized_via", principal.AuthorizedVia,
	)
	http.Redirect(w, r, flow.ReturnTo, http.StatusSeeOther)
}

func (h *HumanAuthHandler) startBeyond(w http.ResponseWriter, r *http.Request) {
	principal, err := h.beyond.AuthenticateRequest(r)
	if err != nil {
		slog.WarnContext(r.Context(), "trusted Beyond identity denied")
		http.Error(w, "This account is not authorized", http.StatusForbidden)
		return
	}
	if err := middleware.SetHumanSessionCookie(w, middleware.HumanSessionOptions{
		Secret:   h.opts.SessionSecret,
		Secure:   h.opts.CookieSecure,
		Duration: h.opts.SessionDuration,
	}, principal); err != nil {
		h.writeInternalError(w, r, "create human session", err)
		return
	}
	slog.InfoContext(r.Context(), "human authentication succeeded",
		"provider", principal.Provider,
		"subject", principal.Subject,
		"user_name", principal.UserName,
		"authorized_via", principal.AuthorizedVia,
	)
	http.Redirect(w, r, localRedirectTarget(r.URL.Query().Get("return_to")), http.StatusSeeOther) //nolint:gosec // G710: target is reduced to a validated local request URI.
}

// Logout validates CSRF, clears the application session, and returns to login.
func (h *HumanAuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if !parseBoundedPostForm(w, r) {
		return
	}
	session, ok := middleware.HumanSessionFromContext(r.Context())
	if !ok || !middleware.ValidateCSRFToken(h.opts.SessionSecret, session.Nonce, r.PostFormValue("csrf_token")) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	middleware.ClearSessionCookie(w, h.opts.CookieSecure)
	slog.InfoContext(r.Context(), "human session ended",
		"provider", session.Provider,
		"subject", session.Subject,
		"user_name", session.UserName,
	)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type oauthFlow struct {
	State     string    `json:"state"`
	Verifier  string    `json:"verifier"`
	Nonce     string    `json:"nonce"`
	ReturnTo  string    `json:"return_to"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Provider  string    `json:"provider"`
}

func (h *HumanAuthHandler) readOAuthFlow(r *http.Request) (oauthFlow, error) {
	cookie, err := r.Cookie(flowCookieName)
	if err != nil {
		return oauthFlow{}, err
	}
	flow, err := decodeOAuthFlow(h.opts.SessionSecret, cookie.Value)
	if err != nil {
		return oauthFlow{}, err
	}
	now := time.Now().UTC()
	if flow.Provider != h.authenticator.Name() || flow.State == "" || flow.Verifier == "" || flow.Nonce == "" || now.Before(flow.IssuedAt) || !now.Before(flow.ExpiresAt) {
		return oauthFlow{}, fmt.Errorf("invalid OAuth flow data")
	}
	return flow, nil
}

func encodeOAuthFlow(secret []byte, flow oauthFlow) (string, error) {
	payload, err := json.Marshal(flow)
	if err != nil {
		return "", fmt.Errorf("marshal OAuth flow: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + signOAuthFlow(secret, encoded), nil
}

func decodeOAuthFlow(secret []byte, value string) (oauthFlow, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || !hmac.Equal([]byte(parts[1]), []byte(signOAuthFlow(secret, parts[0]))) {
		return oauthFlow{}, fmt.Errorf("invalid OAuth flow signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return oauthFlow{}, fmt.Errorf("decode OAuth flow: %w", err)
	}
	var flow oauthFlow
	if err := json.Unmarshal(payload, &flow); err != nil {
		return oauthFlow{}, fmt.Errorf("unmarshal OAuth flow: %w", err)
	}
	return flow, nil
}

func signOAuthFlow(secret []byte, value string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func randomURLToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func (h *HumanAuthHandler) setFlowCookie(w http.ResponseWriter, value string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is false only for explicit loopback development.
		Name:     flowCookieName,
		Value:    value,
		Path:     "/auth/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   h.opts.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
}

func (h *HumanAuthHandler) clearFlowCookie(w http.ResponseWriter) {
	h.setFlowCookie(w, "", time.Unix(1, 0), -1)
}

func (h *HumanAuthHandler) writeInternalError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	slog.ErrorContext(r.Context(), operation, "error", err)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}
