package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEndpointHost(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"203.0.113.10":        "203.0.113.10",
		"203.0.113.10:38823":  "203.0.113.10",
		"vpn.example.com":     "vpn.example.com",
		"vpn.example.com:443": "vpn.example.com",
		"[2001:db8::1]:38823": "2001:db8::1",
		"2001:db8::1":         "2001:db8::1",
	}
	for in, want := range cases {
		in, want := in, want
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, want, endpointHost(in))
		})
	}
}
