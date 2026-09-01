package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/humanauth"
)

const humanSessionVersion = "v1"

// ErrInvalidHumanSession identifies a missing, malformed, tampered, or expired human session.
var ErrInvalidHumanSession = errors.New("invalid human session")

// HumanSessionOptions controls human session cookie creation.
type HumanSessionOptions struct {
	Secret   []byte
	Secure   bool
	Duration time.Duration
}

// HumanSession is a locally validated provider identity with fixed session timing.
type HumanSession struct {
	humanauth.Principal
	Nonce     string    `json:"nonce"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SetHumanSessionCookie creates a signed, absolute-lived application session.
func SetHumanSessionCookie(w http.ResponseWriter, opts HumanSessionOptions, principal humanauth.Principal) error {
	if len(opts.Secret) == 0 {
		return fmt.Errorf("session secret is required")
	}
	if opts.Duration <= 0 {
		return fmt.Errorf("session duration must be greater than 0")
	}
	if !principal.IsValid() {
		return fmt.Errorf("principal provider, subject, and user name are required")
	}

	nonceData := make([]byte, nonceBytes)
	if _, err := rand.Read(nonceData); err != nil {
		return fmt.Errorf("generate session nonce: %w", err)
	}
	now := time.Now().UTC()
	if principal.ProfileCheckedAt.IsZero() {
		principal.ProfileCheckedAt = now
	}
	session := HumanSession{
		Principal: principal,
		Nonce:     base64.RawURLEncoding.EncodeToString(nonceData),
		IssuedAt:  now,
		ExpiresAt: now.Add(opts.Duration),
	}
	value, err := encodeHumanSession(opts.Secret, session)
	if err != nil {
		return err
	}

	cookie := &http.Cookie{ //nolint:gosec // Secure is configurable only for documented loopback development.
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(opts.Duration / time.Second),
		HttpOnly: true,
		Secure:   opts.Secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  session.ExpiresAt,
	}
	http.SetCookie(w, cookie)
	return nil
}

// ReadHumanSession validates and decodes the request's application session.
func ReadHumanSession(r *http.Request, secret []byte, expectedProvider string) (HumanSession, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return HumanSession{}, ErrInvalidHumanSession
	}
	session, err := decodeHumanSession(secret, cookie.Value)
	if err != nil {
		return HumanSession{}, err
	}
	now := time.Now().UTC()
	if session.Provider != expectedProvider || !session.IsValid() || session.Nonce == "" || !session.ExpiresAt.After(session.IssuedAt) || now.Before(session.IssuedAt) || !now.Before(session.ExpiresAt) {
		return HumanSession{}, ErrInvalidHumanSession
	}
	return session, nil
}

func encodeHumanSession(secret []byte, session HumanSession) (string, error) {
	payload, err := json.Marshal(session)
	if err != nil {
		return "", fmt.Errorf("encode human session: %w", err)
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signed := humanSessionVersion + "." + encodedPayload
	signature := humanSessionSignature(secret, signed)
	return signed + "." + signature, nil
}

func decodeHumanSession(secret []byte, value string) (HumanSession, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != humanSessionVersion || parts[1] == "" || parts[2] == "" {
		return HumanSession{}, ErrInvalidHumanSession
	}
	signed := parts[0] + "." + parts[1]
	expected := humanSessionSignature(secret, signed)
	if !hmac.Equal([]byte(parts[2]), []byte(expected)) {
		return HumanSession{}, ErrInvalidHumanSession
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return HumanSession{}, ErrInvalidHumanSession
	}
	var session HumanSession
	if err := json.Unmarshal(payload, &session); err != nil {
		return HumanSession{}, ErrInvalidHumanSession
	}
	return session, nil
}

func humanSessionSignature(secret []byte, value string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
