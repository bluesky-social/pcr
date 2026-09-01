package humanauth_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sarahmaeve/go-prod-change-registry/internal/humanauth"
)

func TestBeyondAuthenticatesVerifiedHeadersAndExactGroup(t *testing.T) {
	t.Parallel()

	provider := humanauth.NewBeyond(humanauth.ProviderOptions{
		AllowedOrgs: []string{"Platform Operators"},
	})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	request.Header.Set("X-Beyond-Email", "Alice@Example.com")
	request.Header.Set("X-Beyond-Name", "Alice Example")
	request.Header.Set("X-Beyond-Groups", "Readers|Platform Operators")

	principal, err := provider.AuthenticateRequest(request)
	if err != nil {
		t.Fatalf("AuthenticateRequest(): %v", err)
	}
	if principal.Provider != "beyond" || principal.Subject != "alice@example.com" {
		t.Errorf("identity = %q/%q, want beyond/alice@example.com", principal.Provider, principal.Subject)
	}
	if principal.UserName != "alice@example.com" || principal.DisplayName != "Alice Example" {
		t.Errorf("profile = %q/%q, want current Beyond values", principal.UserName, principal.DisplayName)
	}
	if principal.AuthorizedVia != "beyond_group:Platform Operators" {
		t.Errorf("AuthorizedVia = %q, want exact allowed group", principal.AuthorizedVia)
	}
}

func TestBeyondFailsClosedWithoutIdentityOrPolicy(t *testing.T) {
	t.Parallel()

	provider := humanauth.NewBeyond(humanauth.ProviderOptions{
		AllowedOrgs: []string{"Operators"},
	})
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{name: "missing email", headers: map[string]string{"X-Beyond-Groups": "Operators"}},
		{name: "malformed email", headers: map[string]string{"X-Beyond-Email": "not-an-email", "X-Beyond-Groups": "Operators"}},
		{name: "multiple at signs", headers: map[string]string{"X-Beyond-Email": "alice@example@com", "X-Beyond-Groups": "Operators"}},
		{name: "case-mismatched group", headers: map[string]string{"X-Beyond-Email": "alice@example.com", "X-Beyond-Groups": "operators"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			for key, value := range tc.headers {
				request.Header.Set(key, value)
			}
			_, err := provider.AuthenticateRequest(request)
			if !errors.Is(err, humanauth.ErrUnauthorized) {
				t.Errorf("AuthenticateRequest() error = %v, want ErrUnauthorized", err)
			}
		})
	}
}
