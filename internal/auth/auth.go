// Package auth issues and verifies the bearer tokens that identify callers.
//
// The HTTP layer depends only on the Verifier interface, so the HS256 tokens
// here can later be replaced by a JWKS/OIDC verifier without touching handlers.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// Issuer is the "iss" claim on every token we mint and the only one we accept.
	Issuer = "notes"

	// MinSecretBytes keeps HS256 keys at least as long as the hash output (RFC 7518 section 3.2).
	MinSecretBytes = 32
)

// ErrInvalidToken is wrapped by every Verify failure, so callers can map all
// of them to one 401 without caring why a token was rejected.
var ErrInvalidToken = errors.New("invalid token")

// Verifier turns a bearer token into the authenticated user name.
type Verifier interface {
	Verify(token string) (user string, err error)
}

// HS256 issues and verifies tokens signed with a shared secret.
type HS256 struct {
	secret []byte
	ttl    time.Duration
	parser *jwt.Parser
	now    func() time.Time // injectable so tests can mint and check expired tokens
}

// NewHS256 returns a signer/verifier. The secret must be at least MinSecretBytes
// long and ttl positive; both are config errors worth failing startup on.
func NewHS256(secret []byte, ttl time.Duration) (*HS256, error) {
	if len(secret) < MinSecretBytes {
		return nil, fmt.Errorf("JWT secret must be at least %d bytes, got %d", MinSecretBytes, len(secret))
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("token TTL must be positive, got %s", ttl)
	}
	h := &HS256{secret: secret, ttl: ttl, now: time.Now}
	h.parser = jwt.NewParser(
		// Pinning the method is what stops "alg":"none" and key-confusion
		// downgrades: the header's alg is never trusted to pick the algorithm.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuer(Issuer),
		jwt.WithTimeFunc(func() time.Time { return h.now() }),
	)
	return h, nil
}

// Issue mints a token for user and reports when it expires.
func (h *HS256) Issue(user string) (token string, expiresAt time.Time, err error) {
	issuedAt := h.now()
	expiresAt = issuedAt.Add(h.ttl)
	claims := jwt.RegisteredClaims{
		Subject:   user,
		Issuer:    Issuer,
		IssuedAt:  jwt.NewNumericDate(issuedAt),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
	}
	token, err = jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(h.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("signing token: %w", err)
	}
	// JWT dates have whole-second precision; report what the token actually says.
	return token, claims.ExpiresAt.Time, nil
}

// Verify checks signature, algorithm, issuer and expiry, and returns the subject.
func (h *HS256) Verify(token string) (string, error) {
	var claims jwt.RegisteredClaims
	_, err := h.parser.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) {
		return h.secret, nil
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if claims.Subject == "" {
		return "", fmt.Errorf("%w: missing sub claim", ErrInvalidToken)
	}
	return claims.Subject, nil
}
