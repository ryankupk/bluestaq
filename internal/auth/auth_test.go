package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func newTestAuth(t *testing.T) *HS256 {
	t.Helper()
	h, err := NewHS256(testSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// sign builds a token with arbitrary claims and method so each case can break exactly one rule.
func sign(t *testing.T, method jwt.SigningMethod, key any, claims jwt.RegisteredClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validClaims() jwt.RegisteredClaims {
	return jwt.RegisteredClaims{
		Subject:   "alice",
		Issuer:    Issuer,
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	h := newTestAuth(t)
	token, expiresAt, err := h.Issue("alice")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Now().Add(time.Hour); expiresAt.Sub(want).Abs() > 5*time.Second {
		t.Errorf("expiresAt = %v, want about %v", expiresAt, want)
	}
	user, err := h.Verify(token)
	if err != nil || user != "alice" {
		t.Fatalf("Verify = %q, %v", user, err)
	}
}

func TestVerifyRejects(t *testing.T) {
	h := newTestAuth(t)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	expired := validClaims()
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	noExp := validClaims()
	noExp.ExpiresAt = nil
	noSub := validClaims()
	noSub.Subject = ""
	wrongIss := validClaims()
	wrongIss.Issuer = "someone-else"

	tests := []struct {
		name  string
		token string
	}{
		{"expired", sign(t, jwt.SigningMethodHS256, testSecret, expired)},
		{"missing exp", sign(t, jwt.SigningMethodHS256, testSecret, noExp)},
		{"wrong secret", sign(t, jwt.SigningMethodHS256, []byte("a-different-secret-of-32-bytes!!!"), validClaims())},
		{"alg none", sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, validClaims())},
		{"wrong alg RS256", sign(t, jwt.SigningMethodRS256, rsaKey, validClaims())},
		{"wrong alg HS512", sign(t, jwt.SigningMethodHS512, testSecret, validClaims())},
		{"missing sub", sign(t, jwt.SigningMethodHS256, testSecret, noSub)},
		{"wrong iss", sign(t, jwt.SigningMethodHS256, testSecret, wrongIss)},
		{"garbage", "not-a-jwt"},
		{"empty", ""},
		{"tampered payload", func() string {
			parts := strings.Split(sign(t, jwt.SigningMethodHS256, testSecret, validClaims()), ".")
			parts[1] = parts[1][:len(parts[1])-2] + "xx"
			return strings.Join(parts, ".")
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, err := h.Verify(tt.token)
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("Verify = %q, %v; want ErrInvalidToken", user, err)
			}
			if user != "" {
				t.Errorf("user = %q on failure", user)
			}
		})
	}
}

func TestVerifyHonorsInjectedClock(t *testing.T) {
	h := newTestAuth(t)
	token, _, err := h.Issue("alice")
	if err != nil {
		t.Fatal(err)
	}
	h.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := h.Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("token past its TTL verified: %v", err)
	}
}

func TestNewHS256Validation(t *testing.T) {
	if _, err := NewHS256([]byte("short"), time.Hour); err == nil {
		t.Error("short secret accepted")
	}
	if _, err := NewHS256(testSecret, 0); err == nil {
		t.Error("zero TTL accepted")
	}
}
