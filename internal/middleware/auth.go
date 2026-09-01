package middleware

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sarahmaeve/go-prod-change-registry/internal/humanauth"
)

type apiPrincipalContextKey struct{}

// authErrorResponse is the JSON structure returned on authentication failure.
type authErrorResponse struct {
	Error authErrorDetail `json:"error"`
}

// authErrorDetail holds the error code and message.
type authErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Auth returns middleware that validates Bearer or Token credentials from the
// Authorization header, session cookies, or query parameter tokens. The tokens slice contains the list of
// valid tokens. If requireForReads is false, GET and HEAD requests are allowed without
// authentication. The sessionSecret is used to validate session cookies.
// Cognitive complexity is in the auth-method fallback chain (bearer header,
// session cookie, query param) plus the read-vs-write toggle. Each branch
// is straightforward; consolidating them would obscure the intent.
func Auth(tokens []string, requireForReads bool, sessionSecret []byte) func(http.Handler) http.Handler {
	return AuthWithTrustedIdentity(tokens, requireForReads, sessionSecret, nil)
}

// AuthWithTrustedIdentity accepts either a trusted-proxy identity or one of
// the configured opaque API tokens. The proxy identity is attached to the
// request context so write handlers can derive immutable event attribution
// from authentication rather than caller-supplied JSON.
//
//nolint:gocognit // The explicit auth fallback chain is easier to audit than indirect dispatch.
func AuthWithTrustedIdentity(tokens []string, requireForReads bool, sessionSecret []byte, principalForRequest RequestPrincipal) func(http.Handler) http.Handler {
	// Store tokens as byte slices for constant-time comparison.
	validTokens := make([][]byte, len(tokens))
	for i, t := range tokens {
		validTokens[i] = []byte(t)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip auth for read methods when not required.
			if !requireForReads && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				next.ServeHTTP(w, r)
				return
			}

			// Beyond strips the app password after validating it and injects its
			// verified identity headers instead. Network isolation is therefore
			// part of this trust boundary: callers must not be able to reach PCR
			// without the trusted proxy.
			if principalForRequest != nil {
				if principal, err := principalForRequest(r); err == nil {
					ctx := context.WithValue(r.Context(), apiPrincipalContextKey{}, principal)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// Check an explicit API credential. Token is supported for deployments
			// behind authentication proxies that reserve Bearer for OIDC JWTs.
			token := extractAPIToken(r)
			if token != "" && ValidateToken([]byte(token), validTokens) {
				next.ServeHTTP(w, r)
				return
			}

			// Check session cookie.
			if len(sessionSecret) > 0 && ValidateSessionCookie(r, sessionSecret) {
				next.ServeHTTP(w, r)
				return
			}

			// Check query param token (backwards compat).
			token = r.URL.Query().Get("token")
			if token != "" && ValidateToken([]byte(token), validTokens) {
				next.ServeHTTP(w, r)
				return
			}

			writeAuthError(r.Context(), w, "missing or malformed Authorization header")
		})
	}
}

// APIPrincipalFromContext returns the identity established by
// AuthWithTrustedIdentity. Opaque legacy API tokens do not carry an identity
// and therefore return ok=false.
func APIPrincipalFromContext(ctx context.Context) (humanauth.Principal, bool) {
	principal, ok := ctx.Value(apiPrincipalContextKey{}).(humanauth.Principal)
	return principal, ok
}

// SecurityHeaders returns middleware that sets common security headers on all responses.
func SecurityHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		})
	}
}

// ValidateToken checks the provided token against the valid tokens list
// using constant-time comparison to prevent timing side-channel attacks.
func ValidateToken(provided []byte, validTokens [][]byte) bool {
	for _, valid := range validTokens {
		if subtle.ConstantTimeCompare(provided, valid) == 1 {
			return true
		}
	}
	return false
}

// extractAPIToken pulls an opaque application token from an Authorization header.
func extractAPIToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	for _, prefix := range []string{"Bearer ", "Token "} {
		if token, ok := strings.CutPrefix(header, prefix); ok {
			return token
		}
	}
	return ""
}

// writeAuthError sends a 401 JSON error response. Encode failures are
// logged but not surfaced to the client -- the status header is already
// committed by then.
func writeAuthError(ctx context.Context, w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)

	resp := authErrorResponse{
		Error: authErrorDetail{
			Code:    "unauthorized",
			Message: message,
		},
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.ErrorContext(ctx, "auth error response encode error", "error", err)
	}
}
