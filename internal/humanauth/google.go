package humanauth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type idTokenVerifier interface {
	Verify(ctx context.Context, raw string, claims any) error
}

type oidcVerifier struct {
	verifier *oidc.IDTokenVerifier
}

func (v *oidcVerifier) Verify(ctx context.Context, raw string, claims any) error {
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return err
	}
	return token.Claims(claims)
}

type googleClaims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	HostedDomain  string `json:"hd"`
	Name          string `json:"name"`
	Nonce         string `json:"nonce"`
}

// Google authenticates users from locally verified OpenID Connect claims.
type Google struct {
	oauth           oauth2.Config
	verifier        idTokenVerifier
	allowedOrgs     []string
	allowedSubjects []string
	allowAny        bool
}

func newGoogle(opts ProviderOptions, endpoint oauth2.Endpoint, verifier idTokenVerifier) *Google {
	return &Google{
		oauth: oauth2.Config{
			ClientID:     opts.ClientID,
			ClientSecret: opts.ClientSecret,
			Endpoint:     endpoint,
			RedirectURL:  opts.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
		verifier:        verifier,
		allowedOrgs:     opts.AllowedOrgs,
		allowedSubjects: opts.AllowedSubjects,
		allowAny:        opts.AllowAny,
	}
}

// Name returns the provider's configuration name.
func (g *Google) Name() string { return "google" }

// IdentityProvider returns the durable provider namespace stored with identities.
func (g *Google) IdentityProvider() string { return "google" }

// AuthorizationURL returns a Google authorization-code URL protected by state, PKCE, and nonce.
func (g *Google) AuthorizationURL(state, codeChallenge, nonce string) string {
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("nonce", nonce),
	}
	if len(g.allowedOrgs) == 1 {
		opts = append(opts, oauth2.SetAuthURLParam("hd", g.allowedOrgs[0]))
	}
	return g.oauth.AuthCodeURL(state, opts...)
}

// Authenticate exchanges a code, verifies the ID token, and enforces access policy.
func (g *Google) Authenticate(ctx context.Context, code, codeVerifier, nonce string) (Principal, error) {
	token, err := g.oauth.Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return Principal{}, fmt.Errorf("exchange Google authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Principal{}, fmt.Errorf("Google token response omitted id_token")
	}
	var claims googleClaims
	if err := g.verifier.Verify(ctx, rawIDToken, &claims); err != nil {
		return Principal{}, fmt.Errorf("verify Google ID token: %w", err)
	}
	if nonce == "" || claims.Nonce != nonce || claims.Subject == "" || claims.Email == "" || !claims.EmailVerified {
		return Principal{}, ErrUnauthorized
	}

	principal := Principal{
		Provider:         "google",
		Subject:          claims.Subject,
		UserName:         strings.ToLower(claims.Email),
		DisplayName:      claims.Name,
		ProfileCheckedAt: time.Now().UTC(),
	}
	qualifiedSubject := principal.NamespacedSubject()
	switch {
	case g.allowAny:
		principal.AuthorizedVia = "any"
	case subjectAllowed(qualifiedSubject, g.allowedSubjects):
		principal.AuthorizedVia = "subject"
	default:
		org, ok := g.allowedOrganization(claims)
		if !ok {
			return Principal{}, ErrUnauthorized
		}
		principal.AuthorizedVia = "google_org:" + org
	}
	return principal, nil
}

func (g *Google) allowedOrganization(claims googleClaims) (string, bool) {
	hostedDomain := strings.ToLower(claims.HostedDomain)
	separator := strings.LastIndex(claims.Email, "@")
	if separator < 0 {
		return "", false
	}
	emailDomain := strings.ToLower(claims.Email[separator+1:])
	for _, org := range g.allowedOrgs {
		if hostedDomain == org && emailDomain == org {
			return org, true
		}
	}
	return "", false
}
