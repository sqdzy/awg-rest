// Package ipam implements deterministic IP allocation over IPv4/IPv6 CIDR
// pools backed by a per-pool cursor and the `unique(node_id, allowed_ip)`
// constraint enforced by the database.
//
// The allocator does NOT require coordination with Postgres for correctness:
// it computes the next candidate from the cursor and lets the unique index
// reject collisions, and the caller retries with the next address. This keeps
// per-request latency O(1) under typical churn while remaining safe under
// concurrent allocation across replicas.
package ipam

import (
	"errors"
	"net/netip"
)

// ErrPoolExhausted is returned when no unused address remains in the pool.
var ErrPoolExhausted = errors.New("ipam: pool exhausted")

// Reserved holds addresses that must be skipped by the allocator (network
// and broadcast for IPv4 /N>30 are also reserved automatically).
type Reserved interface {
	IsReserved(netip.Addr) bool
}

// reservedSet is a slice-backed Reserved.
type reservedSet map[netip.Addr]struct{}

func (r reservedSet) IsReserved(a netip.Addr) bool { _, ok := r[a]; return ok }

// NewReservedSet builds a Reserved over a list of addresses.
func NewReservedSet(addrs ...netip.Addr) Reserved {
	s := make(reservedSet, len(addrs))
	for _, a := range addrs {
		s[a] = struct{}{}
	}
	return s
}

// Allocate returns the next free address starting from cursor inclusive,
// wrapping around the pool exactly once. Reserved (and the network/broadcast
// addresses for IPv4) are skipped.
//
// The returned NextCursor is the address *after* the chosen one and should be
// persisted by the caller in `address_pools.cursor`.
type Allocation struct {
	Address    netip.Addr
	NextCursor netip.Addr
}

func Allocate(pool netip.Prefix, cursor netip.Addr, reserved Reserved) (Allocation, error) {
	if !pool.IsValid() {
		return Allocation{}, errors.New("ipam: invalid pool")
	}
	pool = pool.Masked()

	if !cursor.IsValid() || !pool.Contains(cursor) {
		cursor = firstHost(pool)
	}

	first := firstHost(pool)
	last, err := lastHost(pool)
	if err != nil {
		return Allocation{}, err
	}

	a := cursor
	for steps := 0; steps <= addrCount(pool); steps++ {
		if isUsable(pool, a) && (reserved == nil || !reserved.IsReserved(a)) {
			next := a.Next()
			if !pool.Contains(next) || compareAddr(next, last) > 0 {
				next = first
			}
			return Allocation{Address: a, NextCursor: next}, nil
		}
		a = a.Next()
		if !pool.Contains(a) || compareAddr(a, last) > 0 {
			a = first
		}
	}
	return Allocation{}, ErrPoolExhausted
}

// firstHost returns the first usable host address in the prefix.
//
// IPv4 /31 and /32 are special: /32 has exactly one address (the only host),
// /31 has two (point-to-point per RFC 3021). For /<31 we skip the network
// address. IPv6 has no broadcast, so we just return the prefix base.
func firstHost(p netip.Prefix) netip.Addr {
	a := p.Addr()
	if a.Is4() {
		bits := p.Bits()
		if bits >= 31 {
			return a
		}
		return a.Next()
	}
	return a
}

// lastHost returns the last usable host address in the prefix.
func lastHost(p netip.Prefix) (netip.Addr, error) {
	a := p.Addr()
	bits := a.BitLen() - p.Bits()
	if bits < 0 {
		return netip.Addr{}, errors.New("ipam: prefix wider than address")
	}
	addr := a.As16()
	// Mask out the host portion to all 1s.
	for i := len(addr) - 1; i >= 0 && bits > 0; i-- {
		shift := 8
		if bits < 8 {
			shift = bits
		}
		addr[i] |= byte(1<<shift) - 1
		bits -= shift
	}
	last := netip.AddrFrom16(addr)
	if a.Is4() {
		last = last.Unmap()
		if p.Bits() < 31 {
			// Skip broadcast.
			last = last.Prev()
		}
	}
	return last, nil
}

func isUsable(p netip.Prefix, a netip.Addr) bool {
	if !p.Contains(a) {
		return false
	}
	last, err := lastHost(p)
	if err != nil {
		return false
	}
	first := firstHost(p)
	return compareAddr(a, first) >= 0 && compareAddr(a, last) <= 0
}

func compareAddr(x, y netip.Addr) int { return x.Compare(y) }

// addrCount returns the number of usable host addresses; saturates at math.MaxInt
// to keep the loop bound finite for very large prefixes.
func addrCount(p netip.Prefix) int {
	bits := p.Addr().BitLen() - p.Bits()
	if bits >= 31 {
		return int(^uint(0) >> 1)
	}
	n := 1 << uint(bits)
	if p.Addr().Is4() && p.Bits() < 31 {
		n -= 2
	}
	if n < 0 {
		return 0
	}
	return n
}
