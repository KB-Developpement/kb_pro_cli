package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// signTestToken creates a JWT signed with the given private key for testing.
func signTestToken(t *testing.T, priv ed25519.PrivateKey, c claims, now time.Time) string {
	t.Helper()
	c.IssuedAt = jwt.NewNumericDate(now)
	c.ExpiresAt = jwt.NewNumericDate(now.Add(21 * 24 * time.Hour))
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign test token: %v", err)
	}
	return s
}

// overrideEmbeddedKey replaces the embedded public key with testPub for the duration of the test.
func overrideEmbeddedKey(t *testing.T, testPub ed25519.PublicKey) {
	t.Helper()
	der, _ := x509.MarshalPKIXPublicKey(testPub)
	orig := publicKeyPEM
	publicKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	t.Cleanup(func() { publicKeyPEM = orig })
}

func TestVerifyToken_Success(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	overrideEmbeddedKey(t, pub)

	now := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	c := claims{
		ClientID:    "acme",
		AllowedApps: []string{"kb_pro"},
		Fingerprint: "fp1",
		Tier:        "standard",
	}
	tokenStr := signTestToken(t, priv, c, now)

	got, err := verifyToken(tokenStr)
	if err != nil {
		t.Fatalf("verifyToken: %v", err)
	}
	if got.ClientID != "acme" {
		t.Errorf("ClientID: got %q, want acme", got.ClientID)
	}
	if !isExpired(got, now) {
		// Token valid at issuance.
	}
	if isExpired(got, now.Add(22*24*time.Hour)) == false {
		t.Error("token should be expired after 22 days")
	}
}

func TestVerifyToken_AcceptsExpired(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	overrideEmbeddedKey(t, pub)

	past := time.Now().Add(-30 * 24 * time.Hour)
	c := claims{ClientID: "test"}
	tokenStr := signTestToken(t, priv, c, past)

	_, err = verifyToken(tokenStr)
	if err != nil {
		t.Fatalf("verifyToken rejected expired token (should accept): %v", err)
	}
}

func TestVerifyToken_WrongKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	overrideEmbeddedKey(t, pub2)

	tokenStr := signTestToken(t, priv, claims{ClientID: "test"}, time.Now())
	_, err := verifyToken(tokenStr)
	if err == nil {
		t.Error("expected error for wrong key, got nil")
	}
}

func TestVerifyToken_Garbage(t *testing.T) {
	_, err := verifyToken("notavalidjwt")
	if err == nil {
		t.Error("expected error for garbage token, got nil")
	}
}
