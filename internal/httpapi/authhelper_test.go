package httpapi

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ryankupk/notes/internal/auth"
)

var testSecret = []byte("test-secret-test-secret-test-secret")

func newTestTokens(t *testing.T) *auth.HS256 {
	t.Helper()
	tokens, err := auth.NewHS256(testSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tokens
}

// mintToken signs a token for user with the given lifetime and secret; a
// negative lifetime yields an already-expired token.
func mintToken(t *testing.T, secret []byte, user string, lifetime time.Duration) string {
	t.Helper()
	now := time.Now()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   user,
		Issuer:    auth.Issuer,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(lifetime)),
	}).SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// bearerFor returns an Authorization header value that authenticates as user.
func bearerFor(t *testing.T, user string) string {
	t.Helper()
	return "Bearer " + mintToken(t, testSecret, user, time.Hour)
}

// badCredentials are Authorization values that must all be rejected.
func badCredentials(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"missing":       "",
		"wrong scheme":  "Basic YWxpY2U6cHc=",
		"no token":      "Bearer",
		"garbage":       "Bearer not-a-jwt",
		"bad signature": "Bearer " + mintToken(t, []byte("another-secret-another-secret-123"), "alice", time.Hour),
		"expired":       "Bearer " + mintToken(t, testSecret, "alice", -time.Minute),
	}
}
