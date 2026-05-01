package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func makeValidator(secret string) *HMACValidator {
	return &HMACValidator{
		Secret:      []byte(secret),
		Issuer:      "https://idp.example.com/",
		Audience:    "awg-control-plane",
		AllowedAlgs: []string{"HS256"},
	}
}

func principalForTest() Principal {
	return Principal{
		SubjectID: uuid.New(),
		TenantID:  uuid.New(),
		Roles:     []Role{RoleTenantAdmin},
		Scopes:    []string{"peer:create", "peer:read"},
	}
}

func TestHMACValidator_HappyPath(t *testing.T) {
	t.Parallel()
	v := makeValidator("dev-secret")
	p := principalForTest()
	tok, err := IssueDevToken(v.Secret, v.Issuer, v.Audience, p, time.Hour)
	require.NoError(t, err)

	got, err := v.Validate(context.Background(), tok)
	require.NoError(t, err)
	require.Equal(t, p.SubjectID, got.SubjectID)
	require.Equal(t, p.TenantID, got.TenantID)
	require.True(t, got.HasRole(RoleTenantAdmin))
	require.True(t, got.HasScope("peer:create"))
}

func TestHMACValidator_RejectsAlgNone(t *testing.T) {
	t.Parallel()
	// Build a hand-crafted alg=none token. JWT BCP requires we reject it
	// because "none" is not in AllowedAlgs.
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	body, _ := json.Marshal(map[string]any{
		"iss": "https://idp.example.com/",
		"aud": "awg-control-plane",
		"exp": time.Now().Add(time.Hour).Unix(),
		"sub": uuid.New().String(),
	})
	enc := func(b []byte) string {
		return base64.RawURLEncoding.EncodeToString(b)
	}
	tok := enc(header) + "." + enc(body) + "."
	v := makeValidator("dev-secret")
	_, err := v.Validate(context.Background(), tok)
	require.Error(t, err)
	require.True(t, errors.Is(err, domain.ErrUnauthorized))
}

func TestHMACValidator_RejectsExpired(t *testing.T) {
	t.Parallel()
	v := makeValidator("dev-secret")
	tok, err := IssueDevToken(v.Secret, v.Issuer, v.Audience, principalForTest(), -time.Minute)
	require.NoError(t, err)
	_, err = v.Validate(context.Background(), tok)
	require.Error(t, err)
}

func TestHMACValidator_RejectsBadAudience(t *testing.T) {
	t.Parallel()
	v := makeValidator("dev-secret")
	tok, err := IssueDevToken(v.Secret, v.Issuer, "wrong-aud", principalForTest(), time.Hour)
	require.NoError(t, err)
	_, err = v.Validate(context.Background(), tok)
	require.Error(t, err)
}

func TestHMACValidator_RejectsBadIssuer(t *testing.T) {
	t.Parallel()
	v := makeValidator("dev-secret")
	tok, err := IssueDevToken(v.Secret, "https://other-idp/", v.Audience, principalForTest(), time.Hour)
	require.NoError(t, err)
	_, err = v.Validate(context.Background(), tok)
	require.Error(t, err)
}

func TestHMACValidator_RejectsAlgNotInAllowlist(t *testing.T) {
	t.Parallel()
	v := &HMACValidator{Secret: []byte("dev"), Issuer: "i", Audience: "a", AllowedAlgs: []string{"HS512"}}
	tok, err := IssueDevToken(v.Secret, "i", "a", principalForTest(), time.Hour)
	require.NoError(t, err)
	_, err = v.Validate(context.Background(), tok)
	require.Error(t, err)
}

func TestHMACValidator_AcceptsAudienceArray(t *testing.T) {
	t.Parallel()
	v := makeValidator("s")
	v.Issuer, v.Audience = "iss", "awg"
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": "iss", "aud": []string{"other", "awg"},
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"sub": uuid.New().String(),
	})
	tok.Header["typ"] = "at+jwt"
	signed, err := tok.SignedString(v.Secret)
	require.NoError(t, err)
	_, err = v.Validate(context.Background(), signed)
	require.NoError(t, err)
}

func TestStaticKeyValidator_AcceptsAsymmetricAlgorithms(t *testing.T) {
	t.Parallel()
	p := principalForTest()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	tests := []struct {
		name   string
		method jwt.SigningMethod
		priv   any
		pub    any
	}{
		{name: "RS256", method: jwt.SigningMethodRS256, priv: rsaKey, pub: &rsaKey.PublicKey},
		{name: "ES256", method: jwt.SigningMethodES256, priv: ecdsaKey, pub: &ecdsaKey.PublicKey},
		{name: "EdDSA", method: jwt.SigningMethodEdDSA, priv: edPriv, pub: edPub},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tok := signAccessToken(t, tt.method, tt.priv, "iss", "aud", p, time.Hour)
			v := &StaticKeyValidator{
				DefaultKey:  tt.pub,
				Issuer:      "iss",
				Audience:    "aud",
				AllowedAlgs: []string{tt.name},
			}
			got, err := v.Validate(context.Background(), tok)
			require.NoError(t, err)
			require.Equal(t, p.SubjectID, got.SubjectID)
			require.Equal(t, p.TenantID, got.TenantID)
			require.True(t, got.HasRole(RoleTenantAdmin))
		})
	}
}

func TestStaticKeyValidatorFromPEM(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	v, err := NewStaticKeyValidatorFromPEM(pubPEM, "iss", "aud", []string{"RS256"})
	require.NoError(t, err)
	tok := signAccessToken(t, jwt.SigningMethodRS256, key, "iss", "aud", principalForTest(), time.Hour)
	_, err = v.Validate(context.Background(), tok)
	require.NoError(t, err)
}

func TestStaticKeyValidator_RejectsSymmetricToken(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tok, err := IssueDevToken([]byte("secret"), "iss", "aud", principalForTest(), time.Hour)
	require.NoError(t, err)

	v := &StaticKeyValidator{
		DefaultKey:  &key.PublicKey,
		Issuer:      "iss",
		Audience:    "aud",
		AllowedAlgs: []string{"RS256"},
	}
	_, err = v.Validate(context.Background(), tok)
	require.Error(t, err)
	require.True(t, errors.Is(err, domain.ErrUnauthorized))
}

func TestPrincipal_Roles(t *testing.T) {
	t.Parallel()
	p := Principal{Roles: []Role{RoleTenantAdmin}}
	require.True(t, p.HasRole(RoleTenantAdmin))
	require.False(t, p.HasRole(RolePlatformAdmin))
	require.True(t, p.HasAnyRole(RolePlatformAdmin, RoleTenantAdmin))
}

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()
	p := principalForTest()
	ctx := WithPrincipal(context.Background(), p)
	got, err := FromContext(ctx)
	require.NoError(t, err)
	require.Equal(t, p, got)

	_, err = FromContext(context.Background())
	require.ErrorIs(t, err, domain.ErrUnauthorized)
}

func signAccessToken(t *testing.T, method jwt.SigningMethod, key any, issuer, audience string, p Principal, ttl time.Duration) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":       issuer,
		"aud":       audience,
		"sub":       p.SubjectID.String(),
		"tenant_id": p.TenantID.String(),
		"iat":       now.Unix(),
		"nbf":       now.Unix(),
		"exp":       now.Add(ttl).Unix(),
		"jti":       uuid.New().String(),
		"roles":     rolesToStrings(p.Roles),
		"scope":     strings.Join(p.Scopes, " "),
	}
	tok := jwt.NewWithClaims(method, claims)
	tok.Header["typ"] = "at+jwt"
	signed, err := tok.SignedString(key)
	require.NoError(t, err)
	return signed
}
