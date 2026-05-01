// Package crypto provides WireGuard / AmneziaWG-compatible key generation and
// formatting helpers. WireGuard private keys are 32-byte Curve25519 scalars
// with the standard clamping; public keys are derived via X25519. Keys are
// represented on the wire as standard base64 of the raw 32 bytes (RFC 4648
// without trailing newline).
package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// KeyLen is the raw length of WireGuard keys in bytes.
const KeyLen = 32

// Base64KeyLen is the encoded length (32 bytes -> 44 chars with one '=').
const Base64KeyLen = 44

// KeyPair holds a base64-encoded WireGuard X25519 keypair.
type KeyPair struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

// GenerateKeyPair returns a fresh, properly clamped Curve25519 keypair.
func GenerateKeyPair() (KeyPair, error) {
	var priv [KeyLen]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return KeyPair{}, fmt.Errorf("read random: %w", err)
	}
	clamp(&priv)
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return KeyPair{}, fmt.Errorf("derive public: %w", err)
	}
	return KeyPair{
		PrivateKey: base64.StdEncoding.EncodeToString(priv[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}

// DerivePublicKey returns the base64 X25519 public key for a base64 WG private key.
func DerivePublicKey(privB64 string) (string, error) {
	priv, err := DecodeKey(privB64)
	if err != nil {
		return "", fmt.Errorf("decode private: %w", err)
	}
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return "", fmt.Errorf("derive public: %w", err)
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// GeneratePresharedKey returns a fresh 32-byte preshared key (base64 encoded).
func GeneratePresharedKey() (string, error) {
	var b [KeyLen]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

// DecodeKey parses a base64-encoded WireGuard key.
func DecodeKey(s string) ([KeyLen]byte, error) {
	var out [KeyLen]byte
	if len(s) != Base64KeyLen {
		return out, fmt.Errorf("invalid key length: got %d, want %d", len(s), Base64KeyLen)
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return out, fmt.Errorf("base64 decode: %w", err)
	}
	if len(raw) != KeyLen {
		return out, errors.New("invalid key bytes")
	}
	copy(out[:], raw)
	return out, nil
}

// ValidateKey returns nil if s is a valid base64 32-byte key.
func ValidateKey(s string) error {
	_, err := DecodeKey(s)
	return err
}

// clamp performs the standard Curve25519 scalar clamping.
func clamp(k *[KeyLen]byte) {
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
}
