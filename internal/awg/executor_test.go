package awg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidInterfaceName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"awg0", "awg-0", "awg_0", "awg.0", strings.Repeat("a", 15)} {
		require.True(t, ValidInterfaceName(name), name)
	}

	for _, name := range []string{"", ".", "..", strings.Repeat("a", 16), "../x", "awg/x", "awg x", "awg$x", "awg:0"} {
		require.False(t, ValidInterfaceName(name), name)
	}
}
