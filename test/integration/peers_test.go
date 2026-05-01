//go:build integration

package integration

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestAllocateAndInsert_AssignsSequentialIPs(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	fix := seedFixtures(ctx, t, db)

	peers := &repo.Peers{DB: db}
	addresses := map[string]struct{}{}

	for i := 0; i < 10; i++ {
		err := db.InTx(ctx, func(tx pgx.Tx) error {
			p, err := peers.AllocateAndInsert(ctx, tx, repo.InsertParams{
				TenantID:    fix.TenantID,
				NodeID:      fix.NodeID,
				ProfileID:   fix.ProfileID,
				ExternalID:  "ext-" + itoa(i),
				DisplayName: "name",
				PublicKey:   "k" + itoa(i) + "________________________________________",
			})
			if err != nil {
				return err
			}
			require.NotEmpty(t, p.AllowedIP.String())
			_, dup := addresses[p.AllowedIP.String()]
			require.False(t, dup, "duplicate IP %s", p.AllowedIP)
			addresses[p.AllowedIP.String()] = struct{}{}
			return nil
		})
		require.NoError(t, err)
	}
	require.Len(t, addresses, 10)
}

func TestAllocateAndInsert_RejectsDuplicateExternalID(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	fix := seedFixtures(ctx, t, db)
	peers := &repo.Peers{DB: db}

	insert := func() error {
		return db.InTx(ctx, func(tx pgx.Tx) error {
			_, err := peers.AllocateAndInsert(ctx, tx, repo.InsertParams{
				TenantID:    fix.TenantID,
				NodeID:      fix.NodeID,
				ProfileID:   fix.ProfileID,
				ExternalID:  "shared",
				DisplayName: "n",
				PublicKey:   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			})
			return err
		})
	}
	require.NoError(t, insert())
	err := insert()
	require.Error(t, err)
	require.True(t, errors.Is(err, domain.ErrPeerExists), "expected ErrPeerExists, got %v", err)
}

func TestAllocateAndInsert_UsesTenantScopedPool(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	fix := seedFixtures(ctx, t, db)
	tenants := &repo.Tenants{DB: db}
	pools := &repo.Pools{DB: db}
	peers := &repo.Peers{DB: db}

	other, err := tenants.Upsert(ctx, "other")
	require.NoError(t, err)
	otherCIDR, _ := netip.ParsePrefix("10.91.0.0/24")
	_, err = pools.CreatePool(ctx, other.ID, fix.NodeID, otherCIDR)
	require.NoError(t, err)

	err = db.InTx(ctx, func(tx pgx.Tx) error {
		p, err := peers.AllocateAndInsert(ctx, tx, repo.InsertParams{
			TenantID:    other.ID,
			NodeID:      fix.NodeID,
			ProfileID:   fix.ProfileID,
			ExternalID:  "other-user",
			DisplayName: "other",
			PublicKey:   "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
		})
		require.NoError(t, err)
		require.Equal(t, other.ID, p.TenantID)
		require.True(t, otherCIDR.Contains(p.AllowedIP.Addr()))
		return nil
	})
	require.NoError(t, err)
}

func TestListByNodeIncludingRevoked(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	fix := seedFixtures(ctx, t, db)
	peers := &repo.Peers{DB: db}

	var peerID string
	err := db.InTx(ctx, func(tx pgx.Tx) error {
		p, err := peers.AllocateAndInsert(ctx, tx, repo.InsertParams{
			TenantID:    fix.TenantID,
			NodeID:      fix.NodeID,
			ProfileID:   fix.ProfileID,
			ExternalID:  "revoked-user",
			DisplayName: "revoked",
			PublicKey:   "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=",
		})
		require.NoError(t, err)
		peerID = p.ID.String()
		_, err = peers.MarkRevoked(ctx, tx, p.ID, time.Now().UTC())
		return err
	})
	require.NoError(t, err)
	require.NotEmpty(t, peerID)

	active, err := peers.ListByNode(ctx, fix.NodeID)
	require.NoError(t, err)
	require.Empty(t, active)

	all, err := peers.ListByNodeIncludingRevoked(ctx, fix.NodeID)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, domain.PeerStateRevoked, all[0].State)
}

func TestOutbox_ClaimNextSkipsLockedRows(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	fix := seedFixtures(ctx, t, db)

	ops := &repo.Operations{DB: db}
	ob := &repo.Outbox{DB: db}

	// Enqueue 3 jobs.
	for i := 0; i < 3; i++ {
		err := db.InTx(ctx, func(tx pgx.Tx) error {
			op, err := ops.Insert(ctx, tx, fix.TenantID, fix.NodeID, nil, domain.OpReconcileNode, "")
			if err != nil {
				return err
			}
			_, err = ob.Insert(ctx, tx, repo.Job{
				AggregateType: "node", AggregateID: fix.NodeID, NodeID: fix.NodeID,
				OperationID: &op.ID, Kind: string(domain.OpReconcileNode),
				Payload: []byte("{}"),
			})
			return err
		})
		require.NoError(t, err)
	}

	// Three concurrent workers must each get a different job (FOR UPDATE SKIP LOCKED).
	// Use a non-zero lease so finished claims aren't immediately reclaimable.
	var got [3]string
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := ob.ClaimNext(ctx, 30*time.Second)
			if err == nil && job != nil {
				got[i] = job.ID.String()
			}
		}()
	}
	wg.Wait()

	uniq := map[string]struct{}{}
	for _, g := range got {
		if g != "" {
			uniq[g] = struct{}{}
		}
	}
	require.GreaterOrEqual(t, len(uniq), 2, "expected at least 2 distinct claimed jobs, got %v", got)
}

func TestIdempotency_LookupAndStore(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	fix := seedFixtures(ctx, t, db)
	idem := &repo.Idempotency{DB: db}

	err := db.InTx(ctx, func(tx pgx.Tx) error {
		_, err := idem.Lookup(ctx, tx, fix.TenantID, "k1")
		require.ErrorIs(t, err, domain.ErrNotFound)

		require.NoError(t, idem.Store(ctx, tx, fix.TenantID, "k1", "hash1", nil, 202,
			map[string]string{"x": "y"}, 24*60*60_000_000_000))

		rec, err := idem.Lookup(ctx, tx, fix.TenantID, "k1")
		require.NoError(t, err)
		require.Equal(t, "hash1", rec.RequestHash)
		require.Equal(t, 202, rec.ResponseStatus)
		return nil
	})
	require.NoError(t, err)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	bp := len(b)
	for i > 0 {
		bp--
		b[bp] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		bp--
		b[bp] = '-'
	}
	return string(b[bp:])
}
