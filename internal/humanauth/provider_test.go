package humanauth //nolint:testpackage // Tests inject private provider endpoints and a claims verifier to avoid external network calls.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestGitHubAuthenticate(t *testing.T) {
	t.Parallel()

	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"provider-token","token_type":"bearer"}`))
		case "/user":
			if got := r.Header.Get("Authorization"); got != "Bearer provider-token" {
				t.Errorf("Authorization = %q, want Bearer provider-token", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 12345, "login": "NewHandle", "name": "Alice"})
		case "/user/memberships/orgs/example-inc":
			_ = json.NewEncoder(w).Encode(map[string]string{"state": "active"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	provider := newGitHub(ProviderOptions{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://changes.example.com/auth/callback",
		AllowedOrgs:  []string{"example-inc"},
	}, githubEndpoints{
		OAuth: oauth2.Endpoint{AuthURL: server.URL + "/authorize", TokenURL: server.URL + "/token"},
		API:   server.URL,
	})

	principal, err := provider.Authenticate(t.Context(), "code", "verifier", "")
	if err != nil {
		t.Fatalf("Authenticate(): %v", err)
	}
	if principal.Provider != "github" || principal.Subject != "12345" || principal.UserName != "NewHandle" {
		t.Errorf("Authenticate() principal = %+v, want github/12345/NewHandle", principal)
	}
	if principal.AuthorizedVia != "github_org:example-inc" {
		t.Errorf("AuthorizedVia = %q, want github_org:example-inc", principal.AuthorizedVia)
	}
	close(requests)
}

func TestGitHubAuthorizationFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		membershipStatus int
		want             error
		wantOrgFailure   bool
	}{
		{name: "not a member", membershipStatus: http.StatusNotFound, want: ErrUnauthorized},
		{name: "provider unavailable", membershipStatus: http.StatusTooManyRequests, want: ErrProviderUnavailable, wantOrgFailure: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"access_token":"provider-token","token_type":"bearer"}`))
				case "/user":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 12345, "login": "alice"})
				default:
					w.WriteHeader(tc.membershipStatus)
				}
			}))
			t.Cleanup(server.Close)

			provider := newGitHub(ProviderOptions{
				ClientID: "id", ClientSecret: "secret", RedirectURL: "https://example.com/auth/callback", AllowedOrgs: []string{"example-inc"},
			}, githubEndpoints{OAuth: oauth2.Endpoint{AuthURL: server.URL + "/authorize", TokenURL: server.URL + "/token"}, API: server.URL})
			_, err := provider.Authenticate(t.Context(), "code", "verifier", "")
			if !errors.Is(err, tc.want) {
				t.Errorf("Authenticate() error = %v, want %v", err, tc.want)
			}
			if got := errors.Is(err, ErrOrganizationVerification); got != tc.wantOrgFailure {
				t.Errorf("errors.Is(error, ErrOrganizationVerification) = %t, want %t", got, tc.wantOrgFailure)
			}
		})
	}
}

type fakeIDTokenVerifier struct {
	claims any
	raw    string
}

func (v *fakeIDTokenVerifier) Verify(_ context.Context, raw string, claims any) error {
	v.raw = raw
	payload, err := json.Marshal(v.claims)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, claims)
}

func TestGoogleAuthenticateUsesVerifiedTokenClaims(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"provider-token","token_type":"bearer","id_token":"signed-id-token"}`))
	}))
	t.Cleanup(server.Close)

	verifier := &fakeIDTokenVerifier{claims: googleClaims{
		Subject:       "google-subject",
		Email:         "Alice@Example.com",
		EmailVerified: true,
		HostedDomain:  "Example.COM",
		Name:          "Alice Example",
		Nonce:         "expected-nonce",
	}}
	provider := newGoogle(ProviderOptions{
		ClientID: "id", ClientSecret: "secret", RedirectURL: "https://example.com/auth/callback", AllowedOrgs: []string{"example.com"},
	}, oauth2.Endpoint{AuthURL: server.URL + "/authorize", TokenURL: server.URL}, verifier)

	principal, err := provider.Authenticate(t.Context(), "code", "verifier", "expected-nonce")
	if err != nil {
		t.Fatalf("Authenticate(): %v", err)
	}
	if verifier.raw != "signed-id-token" {
		t.Errorf("verifier token = %q, want signed-id-token", verifier.raw)
	}
	if principal.Provider != "google" || principal.Subject != "google-subject" || principal.UserName != "alice@example.com" {
		t.Errorf("Authenticate() principal = %+v, want normalized Google identity", principal)
	}
	if principal.AuthorizedVia != "google_org:example.com" {
		t.Errorf("AuthorizedVia = %q, want google_org:example.com", principal.AuthorizedVia)
	}
}

func TestGoogleOrganizationRestriction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		claims googleClaims
	}{
		{name: "missing hosted domain", claims: googleClaims{Subject: "1", Email: "alice@example.com", EmailVerified: true, Nonce: "nonce"}},
		{name: "unverified email", claims: googleClaims{Subject: "1", Email: "alice@example.com", HostedDomain: "example.com", Nonce: "nonce"}},
		{name: "suffix lookalike", claims: googleClaims{Subject: "1", Email: "alice@evil-example.com", EmailVerified: true, HostedDomain: "evil-example.com", Nonce: "nonce"}},
		{name: "nonce mismatch", claims: googleClaims{Subject: "1", Email: "alice@example.com", EmailVerified: true, HostedDomain: "example.com", Nonce: "wrong"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"token","token_type":"bearer","id_token":"id-token"}`))
			}))
			t.Cleanup(server.Close)
			provider := newGoogle(ProviderOptions{
				ClientID: "id", ClientSecret: "secret", RedirectURL: "https://example.com/auth/callback", AllowedOrgs: []string{"example.com"},
			}, oauth2.Endpoint{AuthURL: server.URL + "/authorize", TokenURL: server.URL}, &fakeIDTokenVerifier{claims: tc.claims})

			_, err := provider.Authenticate(t.Context(), "code", "verifier", "nonce")
			if !errors.Is(err, ErrUnauthorized) {
				t.Errorf("Authenticate() error = %v, want %v", err, ErrUnauthorized)
			}
		})
	}
}

func TestAuthentikAuthenticateUsesIssuerScopedIdentityAndExactGroups(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"provider-token","token_type":"bearer","id_token":"signed-id-token"}`))
	}))
	t.Cleanup(server.Close)

	const issuer = "https://auth.example.com/application/o/pcr/"
	verifier := &fakeIDTokenVerifier{claims: authentikClaims{
		Issuer:            issuer,
		Subject:           "user-123",
		PreferredUserName: "alice",
		Email:             "alice@example.com",
		EmailVerified:     true,
		Name:              "Alice Example",
		Nonce:             "expected-nonce",
		Groups:            []string{"Platform Operators"},
	}}
	provider := newAuthentik(ProviderOptions{
		ClientID: "id", ClientSecret: "secret", RedirectURL: "https://changes.example.com/auth/callback",
		IssuerURL: issuer, AllowedOrgs: []string{"platform operators", "Platform Operators"},
	}, oauth2.Endpoint{AuthURL: server.URL + "/authorize", TokenURL: server.URL}, verifier)
	authorizationURL, err := url.Parse(provider.AuthorizationURL("state", "challenge", "nonce"))
	if err != nil {
		t.Fatalf("url.Parse(AuthorizationURL()): %v", err)
	}
	if scopes := strings.Fields(authorizationURL.Query().Get("scope")); !slices.Contains(scopes, "groups") {
		t.Errorf("AuthorizationURL() scopes = %v, want groups", scopes)
	}

	principal, err := provider.Authenticate(t.Context(), "code", "verifier", "expected-nonce")
	if err != nil {
		t.Fatalf("Authenticate(): %v", err)
	}
	if principal.Provider != "authentik:"+issuer || principal.Subject != "user-123" || principal.UserName != "alice" {
		t.Errorf("Authenticate() principal = %+v, want issuer-scoped Authentik identity", principal)
	}
	if principal.AuthorizedVia != "authentik_group:Platform Operators" {
		t.Errorf("AuthorizedVia = %q, want exact Authentik group", principal.AuthorizedVia)
	}
}

func TestAuthentikAuthenticationFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		claims authentikClaims
	}{
		{name: "wrong issuer", claims: authentikClaims{Issuer: "https://other.example/", Subject: "user-123", PreferredUserName: "alice", Nonce: "nonce", Groups: []string{"Operators"}}},
		{name: "wrong nonce", claims: authentikClaims{Issuer: "https://auth.example/application/o/pcr/", Subject: "user-123", PreferredUserName: "alice", Nonce: "wrong", Groups: []string{"Operators"}}},
		{name: "missing user name", claims: authentikClaims{Issuer: "https://auth.example/application/o/pcr/", Subject: "user-123", Nonce: "nonce", Groups: []string{"Operators"}}},
		{name: "case-mismatched group", claims: authentikClaims{Issuer: "https://auth.example/application/o/pcr/", Subject: "user-123", PreferredUserName: "alice", Nonce: "nonce", Groups: []string{"operators"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"token","token_type":"bearer","id_token":"id-token"}`))
			}))
			t.Cleanup(server.Close)
			const issuer = "https://auth.example/application/o/pcr/"
			provider := newAuthentik(ProviderOptions{
				ClientID: "id", ClientSecret: "secret", RedirectURL: "https://changes.example/auth/callback",
				IssuerURL: issuer, AllowedOrgs: []string{"Operators"},
			}, oauth2.Endpoint{AuthURL: server.URL + "/authorize", TokenURL: server.URL}, &fakeIDTokenVerifier{claims: tc.claims})

			_, err := provider.Authenticate(t.Context(), "code", "verifier", "nonce")
			if !errors.Is(err, ErrUnauthorized) {
				t.Errorf("Authenticate() error = %v, want %v", err, ErrUnauthorized)
			}
			if tc.name == "case-mismatched group" && !errors.Is(err, ErrGroupUnauthorized) {
				t.Errorf("Authenticate() error = %v, want %v", err, ErrGroupUnauthorized)
			}
		})
	}
}

func TestAuthorizationURLUsesPKCEAndSelectedProviderParameters(t *testing.T) {
	t.Parallel()

	github := newGitHub(ProviderOptions{ClientID: "id", RedirectURL: "https://example.com/auth/callback", AllowedOrgs: []string{"example-inc"}}, githubEndpoints{
		OAuth: oauth2.Endpoint{AuthURL: "https://github.example/authorize", TokenURL: "https://github.example/token"}, //nolint:gosec // Test endpoint, not a credential.
	})
	parsed, err := url.Parse(github.AuthorizationURL("state", "challenge", "ignored"))
	if err != nil {
		t.Fatalf("url.Parse(): %v", err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{"state": "state", "code_challenge": "challenge", "code_challenge_method": "S256"} {
		if got := query.Get(key); got != want {
			t.Errorf("GitHub AuthorizationURL() %s = %q, want %q", key, got, want)
		}
	}
	if scope := query.Get("scope"); !strings.Contains(scope, "read:org") {
		t.Errorf("GitHub AuthorizationURL() scope = %q, want read:org", scope)
	}
}
