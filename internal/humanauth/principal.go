// Package humanauth defines identities verified by a configured human login provider.
package humanauth

import "time"

// Principal is the provider-verified identity and mutable profile snapshot for a human session.
// Provider and Subject form the identity key. Direct providers supply a durable
// subject; Beyond supplies its verified email under the existing proxy contract.
type Principal struct {
	Provider         string    `json:"provider"`
	Subject          string    `json:"subject"`
	UserName         string    `json:"user_name"`
	DisplayName      string    `json:"display_name,omitempty"`
	ProfileCheckedAt time.Time `json:"profile_checked_at"`
	AuthorizedVia    string    `json:"authorized_via"`
}

// NamespacedSubject returns the provider-qualified identity used by allowlists.
func (p Principal) NamespacedSubject() string {
	return p.Provider + ":" + p.Subject
}

// IsValid reports whether the principal contains the fields required for attribution.
func (p Principal) IsValid() bool {
	return p.Provider != "" && p.Subject != "" && p.UserName != ""
}
