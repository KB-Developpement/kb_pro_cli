package license

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// claims holds the JWT payload for a license token.
type claims struct {
	jwt.RegisteredClaims
	ClientID    string   `json:"client_id"`
	AllowedApps []string `json:"allowed_apps"`
	Fingerprint string   `json:"fingerprint"`
	Tier        string   `json:"tier"`
}

// verifyToken parses and verifies a JWT using the embedded public key.
// It does NOT reject expired tokens — callers check expiry separately.
func verifyToken(tokenStr string) (*claims, error) {
	pub, err := parseEmbeddedPublicKey()
	if err != nil {
		return nil, fmt.Errorf("load public key: %w", err)
	}

	var c claims
	_, err = jwt.ParseWithClaims(tokenStr, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return pub, nil
	})
	if err != nil && !errors.Is(err, jwt.ErrTokenExpired) {
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	if c.ClientID == "" {
		return nil, fmt.Errorf("invalid token: missing client_id")
	}
	return &c, nil
}

// isExpired reports whether the token's expiry is before now.
func isExpired(c *claims, now time.Time) bool {
	if c.ExpiresAt == nil {
		return true
	}
	return c.ExpiresAt.Time.Before(now)
}

// parseEmbeddedPublicKey decodes the embedded PEM public key.
func parseEmbeddedPublicKey() (ed25519.PublicKey, error) {
	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in embedded public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse embedded public key: %w", err)
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("embedded key is not ed25519, got %T", key)
	}
	return pub, nil
}
