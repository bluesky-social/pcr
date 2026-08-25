package middleware

import (
	"context"
	"net/http"
	"net/url"

	"github.com/sarah/go-prod-change-registry/internal/humanauth"
)

type humanSessionContextKey struct{}

// RequestPrincipal resolves the identity asserted for the current request by
// a trusted authentication proxy.
type RequestPrincipal func(*http.Request) (humanauth.Principal, error)

// RequireHumanAuth validates a human session and makes it available to dashboard handlers.
func RequireHumanAuth(secret []byte, provider string) func(http.Handler) http.Handler {
	return requireHumanAuth(secret, provider, nil)
}

// RequireBoundHumanAuth validates the local session and binds it to the
// identity asserted by the trusted proxy on every request.
func RequireBoundHumanAuth(secret []byte, provider string, principalForRequest RequestPrincipal) func(http.Handler) http.Handler {
	return requireHumanAuth(secret, provider, principalForRequest)
}

func requireHumanAuth(secret []byte, provider string, principalForRequest RequestPrincipal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session, err := ReadHumanSession(r, secret, provider)
			if err != nil {
				rejectHumanRequest(w, r)
				return
			}
			if principalForRequest != nil {
				principal, err := principalForRequest(r)
				if err != nil || principal.Provider != session.Provider || principal.Subject != session.Subject {
					rejectHumanRequest(w, r)
					return
				}
				session.Principal = principal
			}
			ctx := context.WithValue(r.Context(), humanSessionContextKey{}, session)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func rejectHumanRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		query := url.Values{"return_to": {r.URL.RequestURI()}}
		http.Redirect(w, r, "/login?"+query.Encode(), http.StatusFound)
		return
	}
	http.Error(w, "Authentication required", http.StatusUnauthorized)
}

// HumanSessionFromContext returns the human session established by RequireHumanAuth.
func HumanSessionFromContext(ctx context.Context) (HumanSession, bool) {
	session, ok := ctx.Value(humanSessionContextKey{}).(HumanSession)
	return session, ok
}
