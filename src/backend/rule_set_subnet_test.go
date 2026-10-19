package magitrickle

import (
	"slices"
	"testing"
)

func TestSubnetPrefixes(t *testing.T) {
	for _, test := range []struct {
		rule string
		ipv6 bool
		want []string
	}{
		{"192.0.2.7/24", false, []string{"192.0.2.0/24"}},
		{"192.0.2.1", false, []string{"192.0.2.1/32"}},
		{"::ffff:192.0.2.1", false, []string{"192.0.2.1/32"}},
		{"0.0.0.0/0", false, []string{"0.0.0.0/1", "128.0.0.0/1"}},
		{"2001:db8::7/32", true, []string{"2001:db8::/32"}},
		{"2001:db8::1", true, []string{"2001:db8::1/128"}},
		{"::/0", true, []string{"::/1", "8000::/1"}},
		{"2001:db8::/32", false, nil},
		{"192.0.2.0/24", true, nil},
		{"1.2.3.4/33", false, nil},
		{"fe80::1%eth0", true, nil},
		{"example.com", false, nil},
	} {
		var got []string
		for _, prefix := range subnetPrefixes(test.rule, test.ipv6) {
			got = append(got, prefix.String())
		}
		if !slices.Equal(got, test.want) {
			t.Errorf("%q (ipv6=%t): %v, want %v", test.rule, test.ipv6, got, test.want)
		}
	}
}
