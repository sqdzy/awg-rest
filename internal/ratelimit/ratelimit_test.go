package ratelimit

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTokenBucket_AllowsUntilExhausted(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	tb := NewTokenBucket(3, 1)
	tb.SetNow(func() time.Time { return now })

	for i := 0; i < 3; i++ {
		ok, _ := tb.Allow("k")
		require.True(t, ok, "request %d", i)
	}
	ok, retry := tb.Allow("k")
	require.False(t, ok)
	require.True(t, retry > 0)
}

func TestTokenBucket_RefillsOverTime(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	tb := NewTokenBucket(2, 1) // 1 token / second
	tb.SetNow(func() time.Time { return now })

	tb.Allow("k")
	tb.Allow("k")
	ok, _ := tb.Allow("k")
	require.False(t, ok)

	now = now.Add(time.Second)
	ok, _ = tb.Allow("k")
	require.True(t, ok, "should refill after 1s")
}

func TestTokenBucket_PerKeyIsolation(t *testing.T) {
	t.Parallel()
	tb := NewTokenBucket(1, 1)
	ok, _ := tb.Allow("a")
	require.True(t, ok)
	ok, _ = tb.Allow("b")
	require.True(t, ok, "different key must have its own bucket")
}

func TestTokenBucket_Concurrent(t *testing.T) {
	t.Parallel()
	tb := NewTokenBucket(1000, 1000)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				tb.Allow("shared")
			}
		}()
	}
	wg.Wait() // smoke: must not deadlock or race
}
