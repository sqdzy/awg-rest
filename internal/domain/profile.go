package domain

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ProtocolVersion identifies the AmneziaWG protocol generation. V2 introduces
// S3, S4 and ranges for H1-H4. V3.1 adds header protection, timing ranges,
// random trailers and cookie behavior controls. Profiles are immutable rollout
// units: protocol generations are migrated in parallel, never upgraded in place.
type ProtocolVersion string

const (
	ProtocolV1  ProtocolVersion = "v1"
	ProtocolV2  ProtocolVersion = "v2"
	ProtocolV31 ProtocolVersion = "v3.1"
)

// IntRange represents an inclusive uint32-compatible [Min, Max] range used for
// AmneziaWG header obfuscation parameters H1-H4. int64 storage is intentional:
// PostgreSQL BIGINT and JSON can represent the whole uint32 range without
// signed-32 overflow.
type IntRange struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
}

// ParseIntRange accepts either "1234" or "1234-5678".
func ParseIntRange(s string) (IntRange, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return IntRange{}, fmt.Errorf("empty range")
	}
	if i := strings.IndexByte(s, '-'); i > 0 {
		lo, err := strconv.ParseInt(strings.TrimSpace(s[:i]), 10, 64)
		if err != nil {
			return IntRange{}, fmt.Errorf("invalid lower bound %q: %w", s[:i], err)
		}
		hi, err := strconv.ParseInt(strings.TrimSpace(s[i+1:]), 10, 64)
		if err != nil {
			return IntRange{}, fmt.Errorf("invalid upper bound %q: %w", s[i+1:], err)
		}
		if lo > hi {
			return IntRange{}, fmt.Errorf("range lower bound %d > upper bound %d", lo, hi)
		}
		return IntRange{Min: lo, Max: hi}, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return IntRange{}, fmt.Errorf("invalid integer %q: %w", s, err)
	}
	return IntRange{Min: v, Max: v}, nil
}

// String renders the range back to AmneziaWG-compatible format.
func (r IntRange) String() string {
	if r.Min == r.Max {
		return strconv.FormatInt(r.Min, 10)
	}
	return strconv.FormatInt(r.Min, 10) + "-" + strconv.FormatInt(r.Max, 10)
}

// Uint16Range mirrors upstream u16_range_t. Zero/zero is treated as "unset"
// for optional V3.1 parameters and therefore omitted from rendered configs.
type Uint16Range struct {
	Min uint16 `json:"min"`
	Max uint16 `json:"max"`
}

func (r Uint16Range) String() string {
	if r.Min == r.Max {
		return strconv.FormatUint(uint64(r.Min), 10)
	}
	return strconv.FormatUint(uint64(r.Min), 10) + "-" + strconv.FormatUint(uint64(r.Max), 10)
}

func (r Uint16Range) IsZero() bool { return r.Min == 0 && r.Max == 0 }

func (r Uint16Range) valid() bool { return r.Min <= r.Max }

// ProtocolProfile is the AmneziaWG-specific tunable set. It is versioned —
// changing protocol parameters requires a new profile and client config.
//
// Common constraints:
//   - All interface protocol parameters except Jc/Jmin/Jmax must match between
//     server and client.
//   - H1-H4 fit uint32 and must be pairwise disjoint.
//   - V2 and V3.1 support S3/S4, H ranges, and I1-I5.
//   - V3.1 range-valued timing/padding fields mirror upstream u16_range_t.
//   - RandomTrailers with ranged H1-H3 is intentionally blocked until the
//     upstream packet-classification issue is fixed and verified by real E2E.
type ProtocolProfile struct {
	ID              uuid.UUID       `json:"id"`
	Name            string          `json:"name"`
	ProtocolVersion ProtocolVersion `json:"protocol_version"`

	// Junk-packet randomization (client-only, server ignores).
	Jc   int `json:"jc"`
	Jmin int `json:"jmin"`
	Jmax int `json:"jmax"`

	// Padding sizes.
	S1 int `json:"s1"`
	S2 int `json:"s2"`
	S3 int `json:"s3"`
	S4 int `json:"s4"`

	// Packet header magic ranges.
	H1 IntRange `json:"h1"`
	H2 IntRange `json:"h2"`
	H3 IntRange `json:"h3"`
	H4 IntRange `json:"h4"`

	// V2/V3.1 special-junk CPS packet strings.
	I1 string `json:"i1,omitempty"`
	I2 string `json:"i2,omitempty"`
	I3 string `json:"i3,omitempty"`
	I4 string `json:"i4,omitempty"`
	I5 string `json:"i5,omitempty"`

	// V3.1 interface parameters.
	HeaderProtectionKey    string      `json:"-"`
	ContentPaddingAddition Uint16Range `json:"content_padding_addition"`
	RekeyAfterTime         Uint16Range `json:"rekey_after_time"`
	RekeyTimeout           Uint16Range `json:"rekey_timeout"`
	RejectAfterTime        Uint16Range `json:"reject_after_time"`
	KeepaliveTimeout       Uint16Range `json:"keepalive_timeout"`
	MaxHandshakeAttempts   Uint16Range `json:"max_handshake_attempts"`
	RandomTrailers         bool        `json:"random_trailers"`
	DisableCookies         bool        `json:"disable_cookies"`

	ListenPortPolicy string `json:"listen_port_policy"` // "fixed" | "managed"

	CreatedAt time.Time `json:"created_at"`
}

const (
	// AWGStringMax follows amneziawg-tools MAX_AWG_STRING_LEN (<5 KiB).
	AWGStringMax = 5*1024 - 1
	JcMax        = 10
	JunkSizeMin  = 1
	JunkSizeMax  = 1280
	S1Max        = 1132
	S2Max        = 1188
	S3Max        = 64
	S4Max        = 32
)

// Validate enforces the protocol matrix and project safety gates.
func (p ProtocolProfile) Validate() error {
	var errs ValidationErrors

	if strings.TrimSpace(p.Name) == "" {
		errs = append(errs, ValidationError{Field: "name", Code: "required", Message: "name is required"})
	}

	switch p.ProtocolVersion {
	case ProtocolV1, ProtocolV2, ProtocolV31:
	default:
		errs = append(errs, ValidationError{
			Field: "protocol_version", Code: "unsupported",
			Message: "must be 'v1', 'v2', or 'v3.1'",
		})
		return errs
	}

	checkInt := func(field string, v, lo, hi int) {
		if v < lo || v > hi {
			errs = append(errs, ValidationError{
				Field: field, Code: "out_of_range",
				Message: fmt.Sprintf("must be in [%d, %d], got %d", lo, hi, v),
			})
		}
	}

	checkInt("jc", p.Jc, 0, JcMax)
	if p.Jmin == 0 && p.Jmax == 0 {
		if p.Jc > 0 {
			errs = append(errs, ValidationError{Field: "jmin", Code: "required", Message: "jmin/jmax are required when jc > 0"})
		}
	} else {
		checkInt("jmin", p.Jmin, JunkSizeMin, JunkSizeMax)
		checkInt("jmax", p.Jmax, JunkSizeMin, JunkSizeMax)
	}
	if p.Jmin > p.Jmax {
		errs = append(errs, ValidationError{Field: "jmin", Code: "invalid", Message: "jmin must be <= jmax"})
	}

	checkInt("s1", p.S1, 0, S1Max)
	checkInt("s2", p.S2, 0, S2Max)

	if p.SupportsV2Fields() {
		checkInt("s3", p.S3, 0, S3Max)
		checkInt("s4", p.S4, 0, S4Max)
		for _, ij := range []struct {
			name string
			v    string
		}{{"i1", p.I1}, {"i2", p.I2}, {"i3", p.I3}, {"i4", p.I4}, {"i5", p.I5}} {
			if len(ij.v) > AWGStringMax {
				errs = append(errs, ValidationError{
					Field: ij.name, Code: "too_long",
					Message: fmt.Sprintf("must be at most %d bytes, got %d", AWGStringMax, len(ij.v)),
				})
			} else if ij.v != "" {
				if err := validateSpecialJunk(ij.v); err != nil {
					errs = append(errs, ValidationError{
						Field: ij.name, Code: "invalid_obfuscation",
						Message: err.Error(),
					})
				}
			}
		}
	} else {
		if p.S3 != 0 || p.S4 != 0 {
			errs = append(errs, ValidationError{Field: "s3", Code: "unsupported", Message: "S3/S4 require v2 or v3.1"})
		}
		if p.I1 != "" || p.I2 != "" || p.I3 != "" || p.I4 != "" || p.I5 != "" {
			errs = append(errs, ValidationError{Field: "i1", Code: "unsupported", Message: "I1-I5 require v2 or v3.1"})
		}
	}

	hs := []struct {
		name string
		r    IntRange
	}{{"h1", p.H1}, {"h2", p.H2}, {"h3", p.H3}, {"h4", p.H4}}
	for _, h := range hs {
		if h.r.Min < 0 || h.r.Max < 0 || h.r.Max > 0xFFFFFFFF {
			errs = append(errs, ValidationError{
				Field: h.name, Code: "out_of_range",
				Message: "H values must fit in uint32",
			})
		}
		if h.r.Min > h.r.Max {
			errs = append(errs, ValidationError{Field: h.name, Code: "invalid", Message: "min > max"})
		}
		if p.ProtocolVersion == ProtocolV1 && h.r.Min != h.r.Max {
			errs = append(errs, ValidationError{
				Field: h.name, Code: "unsupported",
				Message: "H ranges require v2 or v3.1; v1 requires single values",
			})
		}
	}

	if rangesOverlap(p.H1, p.H2) || rangesOverlap(p.H1, p.H3) || rangesOverlap(p.H1, p.H4) ||
		rangesOverlap(p.H2, p.H3) || rangesOverlap(p.H2, p.H4) || rangesOverlap(p.H3, p.H4) {
		errs = append(errs, ValidationError{
			Field: "h1", Code: "overlap",
			Message: "H1, H2, H3, H4 must be pairwise disjoint",
		})
	}

	if p.IsV31() {
		if !validBase64Key(p.HeaderProtectionKey) {
			errs = append(errs, ValidationError{
				Field: "header_protection_key", Code: "invalid",
				Message: "must be a base64-encoded 32-byte key for v3.1",
			})
		}
		for _, padding := range []struct {
			name string
			v    int
		}{
			{"s1", p.S1}, {"s2", p.S2}, {"s3", p.S3}, {"s4", p.S4},
		} {
			if padding.v < 12 {
				errs = append(errs, ValidationError{
					Field: padding.name, Code: "header_protection_padding",
					Message: "must be at least 12 when HeaderProtectionKey is enabled",
				})
			}
		}
		for _, rr := range []struct {
			name string
			r    Uint16Range
		}{
			{"content_padding_addition", p.ContentPaddingAddition},
			{"rekey_after_time", p.RekeyAfterTime},
			{"rekey_timeout", p.RekeyTimeout},
			{"reject_after_time", p.RejectAfterTime},
			{"keepalive_timeout", p.KeepaliveTimeout},
			{"max_handshake_attempts", p.MaxHandshakeAttempts},
		} {
			if !rr.r.valid() {
				errs = append(errs, ValidationError{
					Field: rr.name, Code: "invalid",
					Message: "range min must be <= max",
				})
			}
		}
		if p.RandomTrailers && (isRange(p.H1) || isRange(p.H2) || isRange(p.H3)) {
			errs = append(errs, ValidationError{
				Field: "random_trailers", Code: "unsafe_header_range",
				Message: "RandomTrailers with ranged H1-H3 is disabled until upstream packet classification is verified safe",
			})
		}
	} else if p.hasV31OnlyValues() {
		errs = append(errs, ValidationError{
			Field: "header_protection_key", Code: "unsupported",
			Message: "V3.1 parameters require protocol_version 'v3.1'",
		})
	}

	switch p.ListenPortPolicy {
	case "", "fixed", "managed":
	default:
		errs = append(errs, ValidationError{
			Field: "listen_port_policy", Code: "unsupported",
			Message: "must be 'fixed' or 'managed'",
		})
	}

	if errs.Empty() {
		return nil
	}
	return errs
}

func validateSpecialJunk(spec string) error {
	remaining := spec
	totalFixed := 0
	tagCount := 0

	for {
		start := strings.IndexByte(remaining, '<')
		if start == -1 {
			break
		}
		endRel := strings.IndexByte(remaining[start:], '>')
		if endRel == -1 {
			return fmt.Errorf("special-junk tag is missing closing '>'")
		}
		end := start + endRel
		fields := strings.Fields(remaining[start+1 : end])
		if len(fields) == 0 {
			return fmt.Errorf("special-junk tag must not be empty")
		}
		tagCount++

		switch fields[0] {
		case "b":
			if len(fields) != 2 {
				return fmt.Errorf("<b> requires exactly one hex argument")
			}
			raw := strings.TrimPrefix(fields[1], "0x")
			if raw == "" || len(raw)%2 != 0 {
				return fmt.Errorf("<b> requires a non-empty even-length hex argument")
			}
			decoded, err := hex.DecodeString(raw)
			if err != nil {
				return fmt.Errorf("<b> contains invalid hex")
			}
			totalFixed += len(decoded)
		case "t":
			if len(fields) != 1 {
				return fmt.Errorf("<t> does not accept arguments")
			}
			totalFixed += 4
		case "r", "rc", "rd", "dz":
			if len(fields) != 2 {
				return fmt.Errorf("<%s> requires exactly one size argument", fields[0])
			}
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 0 {
				return fmt.Errorf("<%s> size must be a non-negative integer", fields[0])
			}
			if n > JunkSizeMax {
				return fmt.Errorf("<%s> size must be <= %d", fields[0], JunkSizeMax)
			}
			totalFixed += n
		case "d", "ds":
			if len(fields) != 1 {
				return fmt.Errorf("<%s> does not accept arguments", fields[0])
			}
			// Dynamic-data tags depend on the source packet and therefore do
			// not contribute a statically knowable length here.
		default:
			return fmt.Errorf("unknown special-junk tag <%s>", fields[0])
		}
		if totalFixed > JunkSizeMax {
			return fmt.Errorf("special-junk fixed output must be <= %d bytes", JunkSizeMax)
		}
		remaining = remaining[end+1:]
	}

	if tagCount == 0 {
		return fmt.Errorf("special-junk value must contain at least one supported tag")
	}
	return nil
}

func validBase64Key(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	return err == nil && len(decoded) == 32
}

func (p ProtocolProfile) hasV31OnlyValues() bool {
	return p.HeaderProtectionKey != "" ||
		!p.ContentPaddingAddition.IsZero() ||
		!p.RekeyAfterTime.IsZero() ||
		!p.RekeyTimeout.IsZero() ||
		!p.RejectAfterTime.IsZero() ||
		!p.KeepaliveTimeout.IsZero() ||
		!p.MaxHandshakeAttempts.IsZero() ||
		p.RandomTrailers || p.DisableCookies
}

func isRange(r IntRange) bool { return r.Min != r.Max }

// rangesOverlap reports whether two inclusive ranges share any value.
func rangesOverlap(a, b IntRange) bool {
	return a.Min <= b.Max && b.Min <= a.Max
}

// SupportsV2Fields is true for protocol generations that support S3/S4, H
// ranges, and I1-I5.
func (p ProtocolProfile) SupportsV2Fields() bool {
	return p.ProtocolVersion == ProtocolV2 || p.ProtocolVersion == ProtocolV31
}

func (p ProtocolProfile) IsV31() bool { return p.ProtocolVersion == ProtocolV31 }
