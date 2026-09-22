package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validV2() ProtocolProfile {
	return ProtocolProfile{
		Name:            "default-v2",
		ProtocolVersion: ProtocolV2,
		Jc:              5, Jmin: 10, Jmax: 50,
		S1: 130, S2: 89, S3: 31, S4: 18,
		H1: IntRange{Min: 1_000, Max: 2_000},
		H2: IntRange{Min: 3_000, Max: 4_000},
		H3: IntRange{Min: 5_000, Max: 6_000},
		H4: IntRange{Min: 7_000, Max: 8_000},
	}
}

func TestProtocolProfile_ValidateV2_HappyPath(t *testing.T) {
	t.Parallel()
	p := validV2()
	require.NoError(t, p.Validate())
}

func TestProtocolProfile_RejectsJcOverCap(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.Jc = 11
	err := p.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "jc")
}

func TestProtocolProfile_AllowsZeroJunkRangeWhenNoJunkPackets(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.Jc, p.Jmin, p.Jmax = 0, 0, 0
	require.NoError(t, p.Validate())
}

func TestProtocolProfile_RejectsJunkPacketsWithoutSizeRange(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.Jmin = 0
	err := p.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "jmin")
}

func TestProtocolProfile_RejectsJminGreaterThanJmax(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.Jmin, p.Jmax = 128, 64
	err := p.Validate()
	require.Error(t, err)
}

func TestProtocolProfile_RejectsPaddingOverOfficialCaps(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.S2 = 1189
	require.Error(t, p.Validate())

	p = validV2()
	p.S4 = 33
	require.Error(t, p.Validate())
}

func TestProtocolProfile_RejectsHRangesOverlap(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.H2 = IntRange{Min: 500, Max: 1500} // overlaps H1=[1000..2000]
	err := p.Validate()
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "disjoint")
}

func TestProtocolProfile_V1RejectsHRangeWidth(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.ProtocolVersion = ProtocolV1
	// H ranges are still wide -> must fail.
	err := p.Validate()
	require.Error(t, err)
}

func TestProtocolProfile_V1RejectsS3S4(t *testing.T) {
	t.Parallel()
	p := ProtocolProfile{
		Name: "v1-bad", ProtocolVersion: ProtocolV1,
		Jc: 1, Jmin: 64, Jmax: 128, S1: 10, S2: 20, S3: 5, // V2-only
		H1: IntRange{Min: 1, Max: 1}, H2: IntRange{Min: 2, Max: 2}, H3: IntRange{Min: 3, Max: 3}, H4: IntRange{Min: 4, Max: 4},
	}
	err := p.Validate()
	require.Error(t, err)
}

func TestProtocolProfile_V2OnlyIFieldsRejectedOnV1(t *testing.T) {
	t.Parallel()
	p := ProtocolProfile{
		Name: "v1-bad", ProtocolVersion: ProtocolV1,
		Jc: 1, Jmin: 64, Jmax: 128, S1: 10, S2: 20,
		H1: IntRange{Min: 1, Max: 1}, H2: IntRange{Min: 2, Max: 2}, H3: IntRange{Min: 3, Max: 3}, H4: IntRange{Min: 4, Max: 4},
		I1: "junk",
	}
	err := p.Validate()
	require.Error(t, err)
}

func TestProtocolProfile_RejectsTooLongIField(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.I1 = strings.Repeat("a", AWGStringMax+1)
	err := p.Validate()
	require.Error(t, err)
}

func TestProtocolProfile_RejectsBadProtocolVersion(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.ProtocolVersion = "v9000"
	err := p.Validate()
	require.Error(t, err)
}

func TestProtocolProfile_HValueOutOfUint32(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.H1 = IntRange{Min: -1, Max: 0}
	require.Error(t, p.Validate())
}

func TestParseIntRange(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		in       string
		min, max int64
		ok       bool
	}{
		"single":      {"42", 42, 42, true},
		"range":       {"10-20", 10, 20, true},
		"spaces":      {" 1 - 9 ", 1, 9, true},
		"reverse":     {"5-1", 0, 0, false},
		"empty":       {"", 0, 0, false},
		"non-numeric": {"abc", 0, 0, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := ParseIntRange(tc.in)
			if !tc.ok {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.min, r.Min)
			require.Equal(t, tc.max, r.Max)
		})
	}
}

func TestIntRange_String(t *testing.T) {
	t.Parallel()
	require.Equal(t, "5", IntRange{Min: 5, Max: 5}.String())
	require.Equal(t, "5-9", IntRange{Min: 5, Max: 9}.String())
}


func validV31() ProtocolProfile {
	return ProtocolProfile{
		Name:            "default-v31",
		ProtocolVersion: ProtocolV31,
		Jc:              5, Jmin: 10, Jmax: 50,
		S1: 12, S2: 12, S3: 12, S4: 12,
		H1: IntRange{Min: 1, Max: 1},
		H2: IntRange{Min: 2, Max: 2},
		H3: IntRange{Min: 3, Max: 3},
		H4: IntRange{Min: 4, Max: 4},
		I1:              "<packet>",
		HeaderProtectionKey:    "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
		ContentPaddingAddition: Uint16Range{Min: 10, Max: 100},
		RekeyAfterTime:         Uint16Range{Min: 100, Max: 120},
		RekeyTimeout:           Uint16Range{Min: 3, Max: 7},
		RejectAfterTime:        Uint16Range{Min: 150, Max: 180},
		KeepaliveTimeout:       Uint16Range{Min: 5, Max: 15},
		MaxHandshakeAttempts:   Uint16Range{Min: 15, Max: 20},
		RandomTrailers:         true,
		DisableCookies:         true,
	}
}

func TestProtocolProfile_ValidateV31_HappyPath(t *testing.T) {
	t.Parallel()
	require.NoError(t, validV31().Validate())
}

func TestProtocolProfile_V31RejectsInvalidHeaderProtectionKey(t *testing.T) {
	t.Parallel()
	p := validV31()
	p.HeaderProtectionKey = "not-a-key"
	err := p.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "header_protection_key")
}

func TestProtocolProfile_V31RejectsRandomTrailersWithRangedHandshakeHeaders(t *testing.T) {
	t.Parallel()
	p := validV31()
	p.H2 = IntRange{Min: 20, Max: 30}
	err := p.Validate()
	require.Error(t, err)
	ve, ok := err.(ValidationErrors)
	require.True(t, ok)
	var found bool
	for _, item := range ve {
		if item.Code == "unsafe_header_range" {
			found = true
		}
	}
	require.True(t, found, "expected unsafe_header_range validation error")
}

func TestProtocolProfile_V31AllowsRangedH4WithRandomTrailers(t *testing.T) {
	t.Parallel()
	p := validV31()
	p.H4 = IntRange{Min: 40, Max: 50}
	require.NoError(t, p.Validate())
}

func TestProtocolProfile_V2RejectsV31OnlyFields(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.HeaderProtectionKey = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
	err := p.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "header_protection_key")
}

func TestUint16Range_StringAndZero(t *testing.T) {
	t.Parallel()
	require.Equal(t, "5", (Uint16Range{Min: 5, Max: 5}).String())
	require.Equal(t, "5-9", (Uint16Range{Min: 5, Max: 9}).String())
	require.True(t, (Uint16Range{}).IsZero())
	require.False(t, (Uint16Range{Min: 1, Max: 1}).IsZero())
}


func TestProtocolProfile_JSONDoesNotExposeHeaderProtectionKey(t *testing.T) {
	t.Parallel()
	p := validV31()
	b, err := json.Marshal(p)
	require.NoError(t, err)
	require.NotContains(t, string(b), "header_protection_key")
	require.NotContains(t, string(b), p.HeaderProtectionKey)
}


func TestProtocolProfile_V31HeaderProtectionRequiresTwelveBytePaddings(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"s1", "s2", "s3", "s4"} {
		t.Run(field, func(t *testing.T) {
			p := validV31()
			switch field {
			case "s1":
				p.S1 = 11
			case "s2":
				p.S2 = 11
			case "s3":
				p.S3 = 11
			case "s4":
				p.S4 = 11
			}
			err := p.Validate()
			require.Error(t, err)
			ve, ok := err.(ValidationErrors)
			require.True(t, ok)
			var found bool
			for _, item := range ve {
				if item.Field == field && item.Code == "header_protection_padding" {
					found = true
					break
				}
			}
			require.True(t, found, "expected header_protection_padding error for %s", field)
		})
	}
}
