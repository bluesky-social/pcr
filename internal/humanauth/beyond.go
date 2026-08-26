package humanauth

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
)

const (
	beyondEmailHeader  = "X-Beyond-Email"
	beyondNameHeader   = "X-Beyond-Name"
	beyondGroupsHeader = "X-Beyond-Groups"
	maxBeyondEmailLen  = 254
)

// Beyond accepts identity headers verified by a trusted Beyond proxy.
// Deployments must prevent clients from reaching PCR without that proxy.
type Beyond struct {
	allowedGroups   []string
	allowedSubjects []string
	allowAny        bool
}

// NewBeyond creates a trusted-proxy identity provider.
func NewBeyond(opts ProviderOptions) *Beyond {
	return &Beyond{
		allowedGroups:   opts.AllowedOrgs,
		allowedSubjects: opts.AllowedSubjects,
		allowAny:        opts.AllowAny,
	}
}

// Name returns the provider's configuration name.
func (b *Beyond) Name() string { return "beyond" }

// IdentityProvider returns the namespace stored with Beyond identities.
func (b *Beyond) IdentityProvider() string { return "beyond" }

// AuthenticateRequest builds a principal from Beyond's verified headers and
// applies PCR's configured group or individual policy.
func (b *Beyond) AuthenticateRequest(r *http.Request) (Principal, error) {
	email := strings.ToLower(strings.TrimSpace(r.Header.Get(beyondEmailHeader)))
	if !validBeyondEmail(email) {
		return Principal{}, ErrUnauthorized
	}
	principal := Principal{
		Provider:         b.IdentityProvider(),
		Subject:          email,
		UserName:         email,
		DisplayName:      strings.TrimSpace(r.Header.Get(beyondNameHeader)),
		ProfileCheckedAt: time.Now().UTC(),
	}

	switch {
	case b.allowAny:
		principal.AuthorizedVia = "any"
	case subjectAllowed(principal.NamespacedSubject(), b.allowedSubjects):
		principal.AuthorizedVia = "subject"
	default:
		group, ok := b.allowedGroup(r.Header.Get(beyondGroupsHeader))
		if !ok {
			return Principal{}, fmt.Errorf("%w: %w", ErrUnauthorized, ErrGroupUnauthorized)
		}
		principal.AuthorizedVia = "beyond_group:" + group
	}
	return principal, nil
}

func (b *Beyond) allowedGroup(raw string) (string, bool) {
	for group := range strings.SplitSeq(raw, "|") {
		group = strings.TrimSpace(group)
		if slices.Contains(b.allowedGroups, group) {
			return group, true
		}
	}
	return "", false
}

func validBeyondEmail(email string) bool {
	if email == "" || len(email) > maxBeyondEmailLen || strings.Count(email, "@") != 1 || strings.IndexFunc(email, unicode.IsControl) >= 0 {
		return false
	}
	at := strings.LastIndexByte(email, '@')
	return at > 0 && at < len(email)-1
}
