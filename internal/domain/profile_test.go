package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validV2() ProtocolProfile {
	return ProtocolProfile{
		Name:            "default-v2",
		ProtocolVersion: ProtocolV2,
		Jc:              5, Jmin: 64, Jmax: 1000,
		S1: 40, S2: 32, S3: 0, S4: 0,
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

func TestProtocolProfile_RejectsJunkPacketsWithoutOfficialSizeRange(t *testing.T) {
	t.Parallel()
	p := validV2()
	p.Jmin = 50
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
	p.S2 = 65
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
