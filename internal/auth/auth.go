// Package auth implements JWT validation per RFC 8725 (JWT BCP) and a small
// RBAC layer. Only an explicit allowlist of algorithms is accepted; iss, aud,
// exp, nbf, and typ are validated. The package is transport-agnostic.
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Role enumerates the RBAC roles defined in the report.
type Role string

const (
	RolePlatformAdmin    Role = "platform_admin"
	RoleTenantAdmin      Role = "tenant_admin"
	RoleSupportReadOnly  Role = "support_readonly"
	RoleAutomationClient Role = "automation_client"
	RoleNodeAgent        Role = "node_agent"
)

// Principal is the authenticated identity carried in request context.
type Principal struct {
	SubjectID uuid.UUID
	TenantID  uuid.UUID
	Roles     []Role
	Scopes    []string
	Issuer    string
	TokenID   string
}

// HasRole reports whether the principal carries the given role.
func (p Principal) HasRole(r Role) bool {
	for _, x := range p.Roles {
		if x == r {
			return true
		}
	}
	return false
}

// HasAnyRole returns true when the principal carries at least one of the roles.
func (p Principal) HasAnyRole(rs ...Role) bool {
	for _, r := range rs {
		if p.HasRole(r) {
			return true
		}
	}
	return false
}

// HasScope tests whether the principal has the named scope.
func (p Principal) HasScope(s string) bool {
	for _, x := range p.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

// principalCtxKey is the unexported context key for the authenticated principal.
type principalCtxKey struct{}

// WithPrincipal stores the principal in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// FromContext retrieves the principal, or domain.ErrUnauthorized.
func FromContext(ctx context.Context) (Principal, error) {
	p, ok := ctx.Value(principalCtxKey{}).(Principal)
	if !ok {
		return Principal{}, domain.ErrUnauthorized
	}
	return p, nil
}

// Validator validates a bearer token and produces a Principal.
type Validator interface {
	Validate(ctx context.Context, token string) (Principal, error)
}

// HMACValidator is a development-grade validator over HS256/HS384/HS512.
//
// Production deployments should prefer StaticKeyValidator with an asymmetric
// public key so private signing material does not live in API replicas.
type HMACValidator struct {
	Secret      []byte
	Issuer      string
	Audience    string
	AllowedAlgs []string // default: ["HS256"]
	Now         func() time.Time
	Leeway      time.Duration
}

// Validate parses and validates a JWT. Returns a Principal on success.
//
// Per RFC 8725:
//   - Algorithm is checked against an explicit allowlist (defends against alg=none).
//   - `iss` and `aud` are pinned and compared.
//   - `exp` and `nbf` are enforced with optional clock skew leeway.
//   - `typ` SHOULD be `at+jwt` for access tokens; we accept missing or `JWT` too
//     for compatibility, but reject mismatched values when present.
func (v *HMACValidator) Validate(ctx context.Context, raw string) (Principal, error) {
	allowed := v.AllowedAlgs
	if len(allowed) == 0 {
		allowed = []string{"HS256"}
	}

	claims := jwt.MapClaims{}
	tok, err := jwtParser(allowed, v.Leeway, v.Now).ParseWithClaims(raw, &claims, func(tok *jwt.Token) (interface{}, error) {
		return v.Secret, nil
	})
	return principalFromClaims(tok, claims, err, v.Issuer, v.Audience)
}

// StaticKeyValidator validates JWTs signed by asymmetric keys mounted into the
// API process. It supports RSA, ECDSA and Ed25519 public keys and enforces an
// explicit algorithm allowlist.
type StaticKeyValidator struct {
	Keys        map[string]any
	DefaultKey  any
	Issuer      string
	Audience    string
	AllowedAlgs []string // default: ["RS256", "ES256", "EdDSA"]
	Now         func() time.Time
	Leeway      time.Duration
}

// NewStaticKeyValidatorFromPEM builds a validator from one PEM public key or
// certificate. For key rotation with multiple keys, construct StaticKeyValidator
// directly with Keys keyed by JWT kid.
func NewStaticKeyValidatorFromPEM(pemBytes []byte, issuer, audience string, allowedAlgs []string) (*StaticKeyValidator, error) {
	keys, err := ParsePublicKeysPEM(pemBytes)
	if err != nil {
		return nil, err
	}
	if len(keys) != 1 {
		return nil, fmt.Errorf("auth: expected exactly one public key PEM, got %d", len(keys))
	}
	return &StaticKeyValidator{
		DefaultKey:  keys[0],
		Issuer:      issuer,
		Audience:    audience,
		AllowedAlgs: allowedAlgs,
		Leeway:      30 * time.Second,
	}, nil
}

// ParsePublicKeysPEM parses PUBLIC KEY, RSA PUBLIC KEY and CERTIFICATE PEM
// blocks into crypto public keys.
func ParsePublicKeysPEM(pemBytes []byte) ([]any, error) {
	var keys []any
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		key, err := parsePublicKeyBlock(block)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.New("auth: no public keys found in PEM")
	}
	return keys, nil
}

func parsePublicKeyBlock(block *pem.Block) (any, error) {
	switch block.Type {
	case "PUBLIC KEY":
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("auth: parse public key: %w", err)
		}
		return supportedPublicKey(key)
	case "RSA PUBLIC KEY":
		key, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("auth: parse rsa public key: %w", err)
		}
		return key, nil
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("auth: parse certificate: %w", err)
		}
		return supportedPublicKey(cert.PublicKey)
	default:
		return nil, fmt.Errorf("auth: unsupported PEM block %q", block.Type)
	}
}

func supportedPublicKey(key any) (any, error) {
	switch k := key.(type) {
	case *rsa.PublicKey, *ecdsa.PublicKey, ed25519.PublicKey:
		return k, nil
	default:
		return nil, fmt.Errorf("auth: unsupported public key type %T", key)
	}
}

func (v *StaticKeyValidator) Validate(ctx context.Context, raw string) (Principal, error) {
	allowed := v.AllowedAlgs
	if len(allowed) == 0 {
		allowed = []string{"RS256", "ES256", "EdDSA"}
	}
	claims := jwt.MapClaims{}
	tok, err := jwtParser(allowed, v.Leeway, v.Now).ParseWithClaims(raw, &claims, func(tok *jwt.Token) (interface{}, error) {
		key, err := v.keyForToken(tok)
		if err != nil {
			return nil, err
		}
		if !algorithmMatchesKey(tok.Method.Alg(), key) {
			return nil, fmt.Errorf("%w: signing algorithm does not match public key type", domain.ErrUnauthorized)
		}
		return key, nil
	})
	return principalFromClaims(tok, claims, err, v.Issuer, v.Audience)
}

func (v *StaticKeyValidator) keyForToken(tok *jwt.Token) (any, error) {
	if kid, ok := tok.Header["kid"].(string); ok && kid != "" {
		key, ok := v.Keys[kid]
		if !ok {
			return nil, fmt.Errorf("%w: unknown kid", domain.ErrUnauthorized)
		}
		return key, nil
	}
	if v.DefaultKey != nil {
		return v.DefaultKey, nil
	}
	if len(v.Keys) == 1 {
		for _, key := range v.Keys {
			return key, nil
		}
	}
	return nil, fmt.Errorf("%w: missing kid", domain.ErrUnauthorized)
}

func algorithmMatchesKey(alg string, key any) bool {
	switch alg {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512":
		_, ok := key.(*rsa.PublicKey)
		return ok
	case "ES256":
		k, ok := key.(*ecdsa.PublicKey)
		return ok && k.Curve == elliptic.P256()
	case "ES384":
		k, ok := key.(*ecdsa.PublicKey)
		return ok && k.Curve == elliptic.P384()
	case "ES512":
		k, ok := key.(*ecdsa.PublicKey)
		return ok && k.Curve == elliptic.P521()
	case "EdDSA":
		_, ok := key.(ed25519.PublicKey)
		return ok
	default:
		return false
	}
}

func jwtParser(allowed []string, leeway time.Duration, now func() time.Time) *jwt.Parser {
	if now == nil {
		now = time.Now
	}
	return jwt.NewParser(
		jwt.WithValidMethods(allowed),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(leeway),
		jwt.WithTimeFunc(now),
	)
}

func principalFromClaims(tok *jwt.Token, claims jwt.MapClaims, parseErr error, issuer, audience string) (Principal, error) {
	if parseErr != nil {
		return Principal{}, fmt.Errorf("%w: %v", domain.ErrUnauthorized, parseErr)
	}
	if tok == nil || !tok.Valid {
		return Principal{}, domain.ErrUnauthorized
	}

	if typ, ok := tok.Header["typ"].(string); ok {
		switch strings.ToLower(typ) {
		case "", "jwt", "at+jwt":
		default:
			return Principal{}, fmt.Errorf("%w: unexpected typ %q", domain.ErrUnauthorized, typ)
		}
	}

	iss, _ := claims["iss"].(string)
	if issuer != "" && iss != issuer {
		return Principal{}, fmt.Errorf("%w: bad issuer", domain.ErrUnauthorized)
	}
	if audience != "" {
		if !audienceContains(claims["aud"], audience) {
			return Principal{}, fmt.Errorf("%w: bad audience", domain.ErrUnauthorized)
		}
	}

	p := Principal{Issuer: iss}
	if jti, ok := claims["jti"].(string); ok {
		p.TokenID = jti
	}
	if subStr, ok := claims["sub"].(string); ok {
		if id, err := uuid.Parse(subStr); err == nil {
			p.SubjectID = id
		}
	}
	if t, ok := claims["tenant_id"].(string); ok {
		if id, err := uuid.Parse(t); err == nil {
			p.TenantID = id
		}
	}
	for _, r := range stringSlice(claims["roles"]) {
		p.Roles = append(p.Roles, Role(r))
	}
	if s, ok := claims["scope"].(string); ok && s != "" {
		p.Scopes = append(p.Scopes, strings.Fields(s)...)
	}
	for _, s := range stringSlice(claims["scopes"]) {
		p.Scopes = append(p.Scopes, s)
	}

	return p, nil
}

func audienceContains(v any, want string) bool {
	switch x := v.(type) {
	case string:
		return x == want
	case []any:
		for _, y := range x {
			if s, ok := y.(string); ok && s == want {
				return true
			}
		}
	case []string:
		for _, s := range x {
			if s == want {
				return true
			}
		}
	}
	return false
}

func stringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, y := range x {
			if s, ok := y.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{x}
	}
	return nil
}

// IssueDevToken is a helper used by tests and the dev runner to mint tokens
// against an HMACValidator.
func IssueDevToken(secret []byte, issuer, audience string, p Principal, ttl time.Duration) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("auth: empty signing secret")
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   issuer,
		"aud":   audience,
		"sub":   p.SubjectID.String(),
		"iat":   now.Unix(),
		"nbf":   now.Unix(),
		"exp":   now.Add(ttl).Unix(),
		"jti":   uuid.New().String(),
		"roles": rolesToStrings(p.Roles),
		"scope": strings.Join(p.Scopes, " "),
	}
	if p.TenantID != uuid.Nil {
		claims["tenant_id"] = p.TenantID.String()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["typ"] = "at+jwt"
	return tok.SignedString(secret)
}

func rolesToStrings(rs []Role) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}
