//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/stretchr/testify/require"
)

func TestProfiles_V31RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	profiles := &repo.Profiles{DB: db}

	want := domain.ProtocolProfile{
		Name:            "roundtrip-v31",
		ProtocolVersion: domain.ProtocolV31,
		Jc:              5, Jmin: 10, Jmax: 50,
		S1: 12, S2: 12, S3: 12, S4: 12,
		H1:                     domain.IntRange{Min: 1, Max: 1},
		H2:                     domain.IntRange{Min: 2, Max: 2},
		H3:                     domain.IntRange{Min: 3, Max: 3},
		H4:                     domain.IntRange{Min: 4, Max: 4},
		I1:                     "<r 2><b 0x00ff>",
		HeaderProtectionKey:    "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
		ContentPaddingAddition: domain.Uint16Range{Min: 10, Max: 100},
		RekeyAfterTime:         domain.Uint16Range{Min: 100, Max: 120},
		RekeyTimeout:           domain.Uint16Range{Min: 3, Max: 7},
		RejectAfterTime:        domain.Uint16Range{Min: 150, Max: 180},
		KeepaliveTimeout:       domain.Uint16Range{Min: 5, Max: 15},
		MaxHandshakeAttempts:   domain.Uint16Range{Min: 15, Max: 20},
		PersistentKeepalive:    domain.Uint16Range{Min: 25, Max: 35},
		RandomTrailers:         true,
		DisableCookies:         true,
		ListenPortPolicy:       "fixed",
	}

	inserted, err := profiles.Insert(ctx, want)
	require.NoError(t, err)

	got, err := profiles.GetByID(ctx, inserted.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ProtocolV31, got.ProtocolVersion)
	require.Equal(t, want.HeaderProtectionKey, got.HeaderProtectionKey)
	require.Equal(t, want.ContentPaddingAddition, got.ContentPaddingAddition)
	require.Equal(t, want.RekeyAfterTime, got.RekeyAfterTime)
	require.Equal(t, want.RekeyTimeout, got.RekeyTimeout)
	require.Equal(t, want.RejectAfterTime, got.RejectAfterTime)
	require.Equal(t, want.KeepaliveTimeout, got.KeepaliveTimeout)
	require.Equal(t, want.MaxHandshakeAttempts, got.MaxHandshakeAttempts)
	require.Equal(t, want.PersistentKeepalive, got.PersistentKeepalive)
	require.Equal(t, want.RandomTrailers, got.RandomTrailers)
	require.Equal(t, want.DisableCookies, got.DisableCookies)
}

func TestProfiles_V2KeepsV31ColumnsUnset(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	profiles := &repo.Profiles{DB: db}

	inserted, err := profiles.Insert(ctx, domain.ProtocolProfile{
		Name: "legacy-v2", ProtocolVersion: domain.ProtocolV2,
		Jc: 5, Jmin: 10, Jmax: 50, S1: 40, S2: 32,
		H1: domain.IntRange{Min: 1000, Max: 2000},
		H2: domain.IntRange{Min: 3000, Max: 4000},
		H3: domain.IntRange{Min: 5000, Max: 6000},
		H4: domain.IntRange{Min: 7000, Max: 8000},
	})
	require.NoError(t, err)

	got, err := profiles.GetByID(ctx, inserted.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ProtocolV2, got.ProtocolVersion)
	require.Empty(t, got.HeaderProtectionKey)
	require.True(t, got.ContentPaddingAddition.IsZero())
	require.False(t, got.RandomTrailers)
	require.False(t, got.DisableCookies)
}
