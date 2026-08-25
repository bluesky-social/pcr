package humanauth

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type authentikClaims struct {
	Issuer            string   `json:"iss"`
	Subject           string   `json:"sub"`
	PreferredUserName string   `json:"preferred_username"`
	Email             string   `json:"email"`
	EmailVerified     bool     `json:"email_verified"`
	Name              string   `json:"name"`
	Nonce             string   `json:"nonce"`
	Groups            []string `json:"groups"`
}

// Authentik authenticates users from locally verified OpenID Connect claims.
type Authentik struct {
	oauth           oauth2.Config
	verifier        idTokenVerifier
	issuer          string
	allowedGroups   []string
	allowedSubjects []string
	allowAny        bool
}

func newAuthentik(opts ProviderOptions, endpoint oauth2.Endpoint, verifier idTokenVerifier) *Authentik {
	return &Authentik{
		oauth: oauth2.Config{
			ClientID:     opts.ClientID,
			ClientSecret: opts.ClientSecret,
			Endpoint:     endpoint,
			RedirectURL:  opts.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile", "groups"},
		},
		verifier:        verifier,
		issuer:          opts.IssuerURL,
		allowedGroups:   opts.AllowedOrgs,
		allowedSubjects: opts.AllowedSubjects,
		allowAny:        opts.AllowAny,
	}
}

// Name returns the provider's configuration name.
func (a *Authentik) Name() string { return "authentik" }

// IdentityProvider includes the issuer because OIDC subjects are issuer-scoped.
func (a *Authentik) IdentityProvider() string { return "authentik:" + a.issuer }

// AuthorizationURL returns an Authentik authorization-code URL protected by state, PKCE, and nonce.
func (a *Authentik) AuthorizationURL(state, codeChallenge, nonce string) string {
	return a.oauth.AuthCodeURL(
		state,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("nonce", nonce),
	)
}

// Authenticate exchanges a code, verifies the ID token, and enforces subject or group policy.
func (a *Authentik) Authenticate(ctx context.Context, code, codeVerifier, nonce string) (Principal, error) {
	token, err := a.oauth.Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return Principal{}, fmt.Errorf("exchange Authentik authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Principal{}, fmt.Errorf("Authentik token response omitted id_token")
	}
	var claims authentikClaims
	if err := a.verifier.Verify(ctx, rawIDToken, &claims); err != nil {
		return Principal{}, fmt.Errorf("verify Authentik ID token: %w", err)
	}
	if nonce == "" || claims.Nonce != nonce || claims.Issuer != a.issuer || claims.Subject == "" {
		return Principal{}, ErrUnauthorized
	}

	userName := strings.TrimSpace(claims.PreferredUserName)
	if userName == "" && claims.EmailVerified {
		userName = strings.ToLower(strings.TrimSpace(claims.Email))
	}
	if userName == "" {
		return Principal{}, ErrUnauthorized
	}

	principal := Principal{
		Provider:         a.IdentityProvider(),
		Subject:          claims.Subject,
		UserName:         userName,
		DisplayName:      claims.Name,
		ProfileCheckedAt: time.Now().UTC(),
	}
	switch {
	case a.allowAny:
		principal.AuthorizedVia = "any"
	case subjectAllowed(principal.NamespacedSubject(), a.allowedSubjects):
		principal.AuthorizedVia = "subject"
	default:
		group, ok := a.allowedGroup(claims.Groups)
		if !ok {
			return Principal{}, fmt.Errorf("%w: %w", ErrUnauthorized, ErrGroupUnauthorized)
		}
		principal.AuthorizedVia = "authentik_group:" + group
	}
	return principal, nil
}

func (a *Authentik) allowedGroup(groups []string) (string, bool) {
	for _, allowed := range a.allowedGroups {
		if slices.Contains(groups, allowed) {
			return allowed, true
		}
	}
	return "", false
}
