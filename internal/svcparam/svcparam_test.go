package svcparam

import (
	"testing"
)

func mustNormalize(t *testing.T, key string) string {
	t.Helper()
	k, ok := NormalizeKey(key)
	if !ok {
		t.Fatalf("NormalizeKey(%q) failed", key)
	}
	return k
}

func TestNormalizeKey(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"mandatory", "mandatory", true},
		{"ALPN", "alpn", true},
		{"no_default_alpn", "no-default-alpn", true},
		{" no-default-alpn ", "no-default-alpn", true},
		{"ipv4hint", "ipv4hint", true},
		{"IPv6Hint", "ipv6hint", true},
		{"dohpath", "dohpath", true},
		{"port", "port", true},
		{"65", "65", true},
		{" 65 ", "65", true},
		{"065", "65", true},
		{"65535", "65535", true},
		{"65536", "", false},
		{"-1", "", false},
		{"bogus", "", false},
		{"", "", false},
		{"  ", "", false},
	}
	for _, c := range cases {
		got, ok := NormalizeKey(c.in)
		if got != c.want || ok != c.wantOK {
			t.Fatalf("NormalizeKey(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestValidateParam_AcceptsKnownKeys(t *testing.T) {
	valid := []Param{
		{Key: "mandatory", Value: "alpn"},
		{Key: "mandatory", Value: "alpn,port,ipv6hint"},
		{Key: "MANDATORY", Value: "no-default-alpn"},
		{Key: "mandatory", Value: "65"},
		{Key: "alpn", Value: "h2,h3"},
		{Key: "alpn", Value: "h2, h3"},
		{Key: "alpn", Value: "spiffe.io/foo"},
		{Key: "no-default-alpn", Value: ""},
		{Key: "no-default-alpn", Value: "ignored-by-technitium"},
		{Key: "port", Value: "443"},
		{Key: "port", Value: "0"},
		{Key: "port", Value: "65535"},
		{Key: "ipv4hint", Value: "192.168.1.24"},
		{Key: "ipv4hint", Value: "192.168.1.24,10.0.0.2"},
		{Key: "ipv6hint", Value: "2001:db8::1"},
		{Key: "ipv6hint", Value: "2001:db8::1,fd00::5"},
		{Key: "dohpath", Value: "/dns-query{?domain}"},
		{Key: "dohpath", Value: ""},
		{Key: "65", Value: "0102030405"},
		{Key: "65", Value: "01:02:03:04:05"},
		{Key: "65", Value: "0102030405060708"},
		{Key: "5", Value: "0102030405"},
	}
	for _, p := range valid {
		if err := ValidateParam(p.Key, p.Value); err != nil {
			t.Fatalf("ValidateParam(%q, %q) = %v; want nil", p.Key, p.Value, err)
		}
	}
}

func TestValidateParam_RejectsInvalid(t *testing.T) {
	invalid := []Param{
		{Key: "bogus", Value: "x"},
		{Key: "", Value: "x"},
		{Key: "65536", Value: "x"},
		{Key: "mandatory", Value: ""},
		{Key: "mandatory", Value: "bogus"},
		{Key: "mandatory", Value: "alpn,bogus"},
		{Key: "mandatory", Value: ","},
		{Key: "alpn", Value: ""},
		{Key: "alpn", Value: ","},
		{Key: "alpn", Value: " "},
		{Key: "port", Value: ""},
		{Key: "port", Value: "abc"},
		{Key: "port", Value: "65536"},
		{Key: "port", Value: "-1"},
		{Key: "ipv4hint", Value: ""},
		{Key: "ipv4hint", Value: "192.168.1.999"},
		{Key: "ipv4hint", Value: "2001:db8::1"},
		{Key: "ipv4hint", Value: "192.168.1.24,192.168.1.999"},
		{Key: "ipv6hint", Value: ""},
		{Key: "ipv6hint", Value: "192.168.1.24"},
		{Key: "ipv6hint", Value: "2001:db8::1,192.168.1.24"},
		{Key: "65", Value: "xyz"},
		{Key: "65", Value: "0102030"},
		{Key: "65", Value: "abc"},
		{Key: "alpn", Value: "a|b"},
		{Key: "port", Value: "4|3"},
		{Key: "dohpath", Value: "a|b"},
	}
	for _, p := range invalid {
		if err := ValidateParam(p.Key, p.Value); err == nil {
			t.Fatalf("ValidateParam(%q, %q) = nil; want error", p.Key, p.Value)
		}
	}
}

func TestCanonicalValue(t *testing.T) {
	cases := []struct {
		key, value, want string
	}{
		{"mandatory", "port,alpn", "alpn,port"},
		{"mandatory", "ALPN, no_default_alpn", "alpn,no-default-alpn"},
		{"mandatory", "alpn,alpn", "alpn"},
		{"alpn", "h2 , h3", "h2,h3"},
		{"no-default-alpn", "anything", ""},
		{"port", "0443", "443"},
		{"port", "443", "443"},
		{"ipv4hint", "10.0.0.2, 192.168.1.24", "10.0.0.2,192.168.1.24"},
		{"ipv6hint", "2001:0DB8::1,fd00::5", "2001:db8::1,fd00::5"},
		{"dohpath", " /dns-query{?domain} ", "/dns-query{?domain}"},
		{"65", "01:02:03", "010203"},
		{"65", "01:02:03", "010203"},
		{"65", "01:02:03", "010203"},
		{"5", "01:02:03", "010203"},
	}
	for _, c := range cases {
		got := CanonicalValue(mustNormalize(t, c.key), c.value)
		if got != c.want {
			t.Fatalf("CanonicalValue(%q, %q) = %q; want %q", c.key, c.value, got, c.want)
		}
	}
}

func TestCanonicalParams_OrderAndSpellingIndependent(t *testing.T) {
	a := []Param{
		{Key: "alpn", Value: "h2,h3"},
		{Key: "port", Value: "443"},
	}
	b := []Param{
		{Key: "port", Value: "443"},
		{Key: "ALPN", Value: "h2,h3"},
	}
	if CanonicalParams(a) != CanonicalParams(b) {
		t.Fatalf("expected equal canonical forms: %q vs %q", CanonicalParams(a), CanonicalParams(b))
	}
	c := []Param{
		{Key: "alpn", Value: "h3,h2"},
		{Key: "port", Value: "443"},
	}
	if CanonicalParams(a) == CanonicalParams(c) {
		t.Fatalf("ALPN order is significant; %q must not equal %q", CanonicalParams(a), CanonicalParams(c))
	}
}

func TestCanonicalParams_NoDefaultsAlpnAndUnknownKeys(t *testing.T) {
	a := []Param{
		{Key: "no-default-alpn", Value: ""},
		{Key: "65", Value: "01:02:03"},
	}
	if want, got := "65=010203;no-default-alpn=", CanonicalParams(a); got != want {
		t.Fatalf("CanonicalParams = %q; want %q", got, want)
	}
}

func TestEncodeParams(t *testing.T) {
	encoded, ok := EncodeParams([]Param{
		{Key: "alpn", Value: "h2,h3"},
		{Key: "port", Value: "53443"},
	})
	if !ok || encoded != "alpn|h2,h3|port|53443" {
		t.Fatalf("EncodeParams = %q, %v; want %q, true", encoded, ok, "alpn|h2,h3|port|53443")
	}

	encoded, ok = EncodeParams([]Param{{Key: "no_default_alpn", Value: ""}})
	if !ok || encoded != "no-default-alpn|" {
		t.Fatalf("EncodeParams = %q, %v; want %q, true", encoded, ok, "no-default-alpn|")
	}

	if _, ok := EncodeParams([]Param{{Key: "65", Value: "010203"}}); !ok {
		t.Fatalf("expected numeric key codes to encode")
	}

	if _, ok := EncodeParams(nil); ok {
		t.Fatalf("expected empty params to report false so callers send literal \"false\"")
	}
}
