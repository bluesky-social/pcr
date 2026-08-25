package humanauth

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/coreos/go-oidc/v3/oidc"
)

var (
	// ErrUnauthorized means the provider identity did not satisfy the configured access policy.
	ErrUnauthorized = errors.New("human identity is not authorized")
	// ErrProviderUnavailable means required provider identity or membership information could not be verified.
	ErrProviderUnavailable = errors.New("human identity provider is unavailable")
	// ErrOrganizationVerification means a provider failure prevented an organization-membership decision.
	ErrOrganizationVerification = errors.New("human organization membership could not be verified")
	// ErrGroupUnauthorized means an Authentik identity did not include an allowed signed group.
	ErrGroupUnauthorized = errors.New("human identity is not in an allowed group")
)

// ProviderOptions configures one selected human identity provider.
type ProviderOptions struct {
	ClientID        string
	ClientSecret    string
	RedirectURL     string
	IssuerURL       string
	AllowedOrgs     []string
	AllowedSubjects []string
	AllowAny        bool
}

// Authenticator establishes application principals through one configured provider.
type Authenticator interface {
	Name() string
	IdentityProvider() string
	AuthorizationURL(state, codeChallenge, nonce string) string
	Authenticate(ctx context.Context, code, codeVerifier, nonce string) (Principal, error)
}

// New creates the selected production provider and performs any required discovery.
func New(ctx context.Context, name string, opts ProviderOptions) (Authenticator, error) {
	switch name {
	case "github":
		return NewGitHub(opts), nil
	case "google":
		provider, err := oidc.NewProvider(ctx, "https://accounts.google.com")
		if err != nil {
			return nil, fmt.Errorf("discover Google OpenID Connect provider: %w", err)
		}
		verifier := &oidcVerifier{verifier: provider.Verifier(&oidc.Config{ClientID: opts.ClientID})}
		return newGoogle(opts, provider.Endpoint(), verifier), nil
	case "authentik":
		provider, err := oidc.NewProvider(ctx, opts.IssuerURL)
		if err != nil {
			return nil, fmt.Errorf("discover Authentik OpenID Connect provider: %w", err)
		}
		verifier := &oidcVerifier{verifier: provider.Verifier(&oidc.Config{ClientID: opts.ClientID})}
		return newAuthentik(opts, provider.Endpoint(), verifier), nil
	default:
		return nil, fmt.Errorf("unsupported human authentication provider %q", name)
	}
}

func subjectAllowed(subject string, allowed []string) bool {
	return slices.Contains(allowed, subject)
}
