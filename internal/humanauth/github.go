package humanauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

type githubEndpoints struct {
	OAuth oauth2.Endpoint
	API   string
}

// GitHub authenticates users and, when configured, verifies active organization membership.
type GitHub struct {
	oauth           oauth2.Config
	apiURL          string
	allowedOrgs     []string
	allowedSubjects []string
	allowAny        bool
}

// NewGitHub creates a GitHub authenticator using github.com endpoints.
func NewGitHub(opts ProviderOptions) *GitHub {
	return newGitHub(opts, githubEndpoints{OAuth: github.Endpoint, API: "https://api.github.com"})
}

func newGitHub(opts ProviderOptions, endpoints githubEndpoints) *GitHub {
	scopes := []string{"read:user"}
	if len(opts.AllowedOrgs) > 0 {
		scopes = append(scopes, "read:org")
	}
	return &GitHub{
		oauth: oauth2.Config{
			ClientID:     opts.ClientID,
			ClientSecret: opts.ClientSecret,
			Endpoint:     endpoints.OAuth,
			RedirectURL:  opts.RedirectURL,
			Scopes:       scopes,
		},
		apiURL:          strings.TrimRight(endpoints.API, "/"),
		allowedOrgs:     opts.AllowedOrgs,
		allowedSubjects: opts.AllowedSubjects,
		allowAny:        opts.AllowAny,
	}
}

// Name returns the provider's configuration name.
func (g *GitHub) Name() string { return "github" }

// IdentityProvider returns the durable provider namespace stored with identities.
func (g *GitHub) IdentityProvider() string { return "github" }

// AuthorizationURL returns a GitHub authorization-code URL protected by state and PKCE.
func (g *GitHub) AuthorizationURL(state, codeChallenge, _ string) string {
	return g.oauth.AuthCodeURL(
		state,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

// Authenticate exchanges a code, fetches the current profile, and enforces access policy.
func (g *GitHub) Authenticate(ctx context.Context, code, codeVerifier, _ string) (Principal, error) {
	token, err := g.oauth.Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return Principal{}, fmt.Errorf("exchange GitHub authorization code: %w", err)
	}
	client := g.oauth.Client(ctx, token)

	profile, err := g.profile(ctx, client)
	if err != nil {
		return Principal{}, err
	}
	principal := Principal{
		Provider:         "github",
		Subject:          strconv.FormatInt(profile.ID, 10),
		UserName:         profile.Login,
		DisplayName:      profile.Name,
		ProfileCheckedAt: time.Now().UTC(),
	}
	if !principal.IsValid() {
		return Principal{}, fmt.Errorf("GitHub returned an incomplete identity")
	}

	qualifiedSubject := principal.NamespacedSubject()
	switch {
	case g.allowAny:
		principal.AuthorizedVia = "any"
	case subjectAllowed(qualifiedSubject, g.allowedSubjects):
		principal.AuthorizedVia = "subject"
	default:
		org, err := g.activeOrganization(ctx, client)
		if err != nil {
			return Principal{}, err
		}
		principal.AuthorizedVia = "github_org:" + org
	}
	return principal, nil
}

type githubProfile struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

func (g *GitHub) profile(ctx context.Context, client *http.Client) (githubProfile, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, g.apiURL+"/user", nil)
	if err != nil {
		return githubProfile{}, fmt.Errorf("create GitHub profile request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return githubProfile{}, fmt.Errorf("%w: fetch GitHub profile: %v", ErrProviderUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return githubProfile{}, fmt.Errorf("%w: GitHub profile returned HTTP %d", ErrProviderUnavailable, response.StatusCode)
	}
	var profile githubProfile
	if err := json.NewDecoder(response.Body).Decode(&profile); err != nil {
		return githubProfile{}, fmt.Errorf("%w: decode GitHub profile: %v", ErrProviderUnavailable, err)
	}
	return profile, nil
}

func (g *GitHub) activeOrganization(ctx context.Context, client *http.Client) (string, error) {
	for _, org := range g.allowedOrgs {
		path := g.apiURL + "/user/memberships/orgs/" + url.PathEscape(org)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		if err != nil {
			return "", fmt.Errorf("%w: %w: create GitHub organization request: %v", ErrProviderUnavailable, ErrOrganizationVerification, err)
		}
		response, err := client.Do(request)
		if err != nil {
			return "", fmt.Errorf("%w: %w: verify GitHub organization: %v", ErrProviderUnavailable, ErrOrganizationVerification, err)
		}
		var membership struct {
			State string `json:"state"`
		}
		if response.StatusCode == http.StatusOK {
			decodeErr := json.NewDecoder(response.Body).Decode(&membership)
			_ = response.Body.Close()
			if decodeErr != nil {
				return "", fmt.Errorf("%w: %w: decode GitHub organization membership: %v", ErrProviderUnavailable, ErrOrganizationVerification, decodeErr)
			}
			if membership.State == "active" {
				return org, nil
			}
			continue
		}
		_ = response.Body.Close()
		if response.StatusCode == http.StatusNotFound {
			continue
		}
		return "", fmt.Errorf("%w: %w: GitHub organization membership returned HTTP %d", ErrProviderUnavailable, ErrOrganizationVerification, response.StatusCode)
	}
	return "", ErrUnauthorized
}
