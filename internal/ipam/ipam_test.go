package ipam

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	require.NoError(t, err)
	return p
}

func TestAllocate_IPv4SkipsNetworkAndBroadcast(t *testing.T) {
	t.Parallel()
	pool := mustPrefix(t, "10.0.0.0/30") // hosts: .1, .2
	a, err := Allocate(pool, netip.Addr{}, nil)
	require.NoError(t, err)
	require.Equal(t, "10.0.0.1", a.Address.String())
	a2, err := Allocate(pool, a.NextCursor, nil)
	require.NoError(t, err)
	require.Equal(t, "10.0.0.2", a2.Address.String())

	// Both used -> exhausted.
	used := NewReservedSet(netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2"))
	_, err = Allocate(pool, a2.NextCursor, used)
	require.ErrorIs(t, err, ErrPoolExhausted)
}

func TestAllocate_SkipsReserved(t *testing.T) {
	t.Parallel()
	pool := mustPrefix(t, "10.90.0.0/24")
	used := NewReservedSet(
		netip.MustParseAddr("10.90.0.1"),
		netip.MustParseAddr("10.90.0.2"),
		netip.MustParseAddr("10.90.0.3"),
	)
	a, err := Allocate(pool, netip.Addr{}, used)
	require.NoError(t, err)
	require.Equal(t, "10.90.0.4", a.Address.String())
}

func TestAllocate_WrapsAround(t *testing.T) {
	t.Parallel()
	pool := mustPrefix(t, "10.0.0.0/29") // hosts: .1..6
	cursor := netip.MustParseAddr("10.0.0.6")
	used := NewReservedSet(
		netip.MustParseAddr("10.0.0.6"),
		netip.MustParseAddr("10.0.0.2"),
		netip.MustParseAddr("10.0.0.3"),
	)
	a, err := Allocate(pool, cursor, used)
	require.NoError(t, err)
	// First non-reserved starting from .6 wrapping = .1
	require.Equal(t, "10.0.0.1", a.Address.String())
}

func TestAllocate_IPv6(t *testing.T) {
	t.Parallel()
	pool := mustPrefix(t, "fd00:abcd::/126") // 4 addrs, all usable
	a, err := Allocate(pool, netip.Addr{}, nil)
	require.NoError(t, err)
	require.Equal(t, "fd00:abcd::", a.Address.String())
	a2, err := Allocate(pool, a.NextCursor, nil)
	require.NoError(t, err)
	require.Equal(t, "fd00:abcd::1", a2.Address.String())
}

func TestAllocate_Slash32(t *testing.T) {
	t.Parallel()
	pool := mustPrefix(t, "10.10.10.10/32")
	a, err := Allocate(pool, netip.Addr{}, nil)
	require.NoError(t, err)
	require.Equal(t, "10.10.10.10", a.Address.String())
}

func TestAllocate_LinearIssuesAreUnique(t *testing.T) {
	t.Parallel()
	pool := mustPrefix(t, "10.0.0.0/28") // 14 hosts
	seen := map[string]struct{}{}
	used := reservedSet{}
	cursor := netip.Addr{}
	for i := 0; i < 14; i++ {
		a, err := Allocate(pool, cursor, used)
		require.NoError(t, err)
		_, dup := seen[a.Address.String()]
		require.False(t, dup)
		seen[a.Address.String()] = struct{}{}
		used[a.Address] = struct{}{}
		cursor = a.NextCursor
	}
	_, err := Allocate(pool, cursor, used)
	require.ErrorIs(t, err, ErrPoolExhausted)
}
