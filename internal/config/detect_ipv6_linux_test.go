//go:build linux

package config

import (
	"net"
	"testing"
)

// This fixture mirrors a real Debian host's /proc/net/if_inet6: a loopback,
// a link-local, and one address family (here two, ULA + GUA, to match a
// real dual-stack host) each with a temporary/privacy pairing alongside its
// stable EUI-64 counterpart. The stable addresses' low 64 bits
// (be24:11ff:fe93:59e7) and the MAC they're derived from (bc:24:11:93:59:e7)
// are the real values confirmed against a live Debian 13 box's `ip addr`
// output; the prefixes themselves (fdaa:bbcc:ddee:1::/64, the RFC 3849
// documentation prefix 2001:db8:dead:beef::/64) are illustrative, not the
// live box's actual prefixes, since reading its /proc/net/if_inet6 directly
// was not possible from this environment (see the PR/report notes).
//
// Field order: <32-hex address><ifindex hex><prefixlen hex><scope hex><flags hex><device>
const sampleIfInet6 = `00000000000000000000000000000001 01 80 10 80       lo
fe80000000000000be2411fffe9359e7 02 40 20 80       eth0
fdaabbccddee0001be2411fffe9359e7 02 40 00 00       eth0
fdaabbccddee00011122334455667788 02 40 00 01       eth0
20010db8deadbeefbe2411fffe9359e7 02 40 00 00       eth0
20010db8deadbeefaabbccddeeff0011 02 40 00 01       eth0
`

var testMAC = net.HardwareAddr{0xbc, 0x24, 0x11, 0x93, 0x59, 0xe7}

func macOfEth0(ifindex int) net.HardwareAddr {
	if ifindex == 2 {
		return testMAC
	}
	return nil
}

func TestEUI64FromMAC_MatchesKnownGoodExample(t *testing.T) {
	// bc:24:11:93:59:e7 -> flip the universal/local bit of the first octet
	// (0xbc ^ 0x02 = 0xbe), splice in ff:fe -> be:24:11:ff:fe:93:59:e7.
	// Confirmed against a live Debian 13 host's actual stable SLAAC
	// addresses, which really do end in be24:11ff:fe93:59e7 for this MAC.
	id, ok := eui64FromMAC(testMAC)
	if !ok {
		t.Fatalf("eui64FromMAC rejected a well-formed 6-byte MAC")
	}
	got := net.HardwareAddr(id[:]).String()
	want := "be:24:11:ff:fe:93:59:e7"
	if got != want {
		t.Fatalf("eui64FromMAC(%v) = %s, want %s", testMAC, got, want)
	}
}

func TestEUI64FromMAC_RejectsWrongLength(t *testing.T) {
	if _, ok := eui64FromMAC(net.HardwareAddr{0x01, 0x02, 0x03}); ok {
		t.Fatalf("expected eui64FromMAC to reject a non-6-byte MAC")
	}
}

func TestParseIfInet6_ParsesEveryField(t *testing.T) {
	addrs := parseIfInet6(sampleIfInet6)
	if len(addrs) != 6 {
		t.Fatalf("expected 6 parsed addresses, got %d: %+v", len(addrs), addrs)
	}

	lo := addrs[0]
	if lo.ip.String() != "::1" {
		t.Errorf("lo: expected ::1, got %s", lo.ip)
	}
	if lo.ifindex != 1 || lo.scope != 0x10 || lo.flags != 0x80 || lo.device != "lo" {
		t.Errorf("lo: unexpected fields: %+v", lo)
	}

	stableULA := addrs[2]
	if stableULA.ip.String() != "fdaa:bbcc:ddee:1:be24:11ff:fe93:59e7" {
		t.Errorf("unexpected stable ULA address: %s", stableULA.ip)
	}
	if stableULA.scope != scopeGlobal || stableULA.flags != 0x00 || stableULA.ifindex != 2 {
		t.Errorf("stable ULA: unexpected fields: %+v", stableULA)
	}

	tempULA := addrs[3]
	if tempULA.flags != ifaFTemporary {
		t.Errorf("temp ULA: expected the temporary flag, got flags=%#x", tempULA.flags)
	}
}

func TestParseIfInet6_SkipsMalformedLines(t *testing.T) {
	data := "not a valid line at all\n" + sampleIfInet6 + "\ntoo few fields\n"
	addrs := parseIfInet6(data)
	if len(addrs) != 6 {
		t.Fatalf("expected malformed lines to be skipped, got %d addresses", len(addrs))
	}
}

func TestPickStableIPv6_PrefersEUI64SLAACOverTemporary(t *testing.T) {
	addrs := parseIfInet6(sampleIfInet6)
	got := pickStableIPv6(addrs, macOfEth0)
	// Both the ULA and GUA stable addresses qualify for tier 2; the ULA
	// wins via preferULA's explicit tiebreak (see
	// TestPickStableIPv6_PrefersULAOverGUAEvenWhenGUAComesFirstInFile for
	// the case where file order alone would pick the GUA). The important
	// assertion here is what it must NOT be: a temporary address, a
	// link-local address, or loopback.
	want := "fdaa:bbcc:ddee:1:be24:11ff:fe93:59e7"
	if got != want {
		t.Fatalf("pickStableIPv6 = %q, want %q", got, want)
	}
}

func TestPickStableIPv6_PermanentBeatsEUI64SLAAC(t *testing.T) {
	// A DHCPv6/static address (IFA_F_PERMANENT) must win over the stable
	// SLAAC address even though both are eligible -- tier 1 beats tier 2.
	const dhcpLine = "fdaabbccddee0001000000000000dead 02 40 00 80       eth0\n"
	addrs := parseIfInet6(sampleIfInet6 + dhcpLine)
	got := pickStableIPv6(addrs, macOfEth0)
	want := "fdaa:bbcc:ddee:1::dead"
	if got != want {
		t.Fatalf("pickStableIPv6 = %q, want the permanent address %q", got, want)
	}
}

func TestPickStableIPv6_ExcludesLinkLocalAndLoopbackEvenIfPermanent(t *testing.T) {
	// lo (::1) and the link-local address are both flagged permanent in the
	// fixture -- if scope filtering didn't work, tier 1 would wrongly pick
	// one of them ahead of any global address.
	onlyUnusable := `00000000000000000000000000000001 01 80 10 80       lo
fe80000000000000be2411fffe9359e7 02 40 20 80       eth0
`
	got := pickStableIPv6(parseIfInet6(onlyUnusable), macOfEth0)
	if got != "" {
		t.Fatalf("expected no usable address, got %q", got)
	}
}

func TestPickStableIPv6_ReturnsBlankWhenOnlyTemporaryAddressesExist(t *testing.T) {
	onlyTemporary := `fdaabbccddee00011122334455667788 02 40 00 01       eth0
`
	got := pickStableIPv6(parseIfInet6(onlyTemporary), macOfEth0)
	if got != "" {
		t.Fatalf("expected blank when nothing stable exists, got %q", got)
	}
}

func TestPickStableIPv6_ReturnsBlankWhenMACIsUnknown(t *testing.T) {
	noMAC := func(int) net.HardwareAddr { return nil }
	got := pickStableIPv6(parseIfInet6(sampleIfInet6), noMAC)
	if got != "" {
		t.Fatalf("expected blank when the owning interface's MAC can't be resolved, got %q", got)
	}
}

func TestPickStableIPv6_DeprecatedAndTentativeAreExcluded(t *testing.T) {
	data := `fdaabbccddee0001be2411fffe9359e7 02 40 00 20       eth0
20010db8deadbeefbe2411fffe9359e7 02 40 00 40       eth0
`
	got := pickStableIPv6(parseIfInet6(data), macOfEth0)
	if got != "" {
		t.Fatalf("expected deprecated/tentative addresses to be excluded, got %q", got)
	}
}

// realIfInet6FromLiveHost is the literal, unmodified /proc/net/if_inet6
// output read from a real Debian 13 host (MAC bc:24:11:93:59:e7 on eth0,
// same host referenced throughout this file). It matters as a fixture for
// one specific reason: every global-scope line here -- the stable GUA, the
// temporary GUA, the stable ULA, AND the temporary ULA -- reports flags=00.
// There is nothing in the flags to tell stable from temporary on this real
// kernel/config; only the EUI-64-vs-MAC comparison in tier 2 does that (see
// the note on notStableEnough above pickStableIPv6). It also happens to
// list the GUA before the ULA, which is exactly the case that would pick
// the wrong (public) address if preferULA didn't exist.
const realIfInet6FromLiveHost = `26038002790022f1be2411fffe9359e7 02 40 00 00     eth0
26038002790022f17848c633b5ded169 02 40 00 00     eth0
00000000000000000000000000000001 01 80 10 80       lo
fdea894ad11a0001666905e494a1ab7e 02 40 00 00     eth0
fe8000000000000068491fda289380d5 02 40 20 80     eth0
fdea894ad11a0001be2411fffe9359e7 02 40 00 00     eth0
`

func TestPickStableIPv6_RealHostData_AllZeroFlagsStillDiscriminatesViaEUI64(t *testing.T) {
	got := pickStableIPv6(parseIfInet6(realIfInet6FromLiveHost), macOfEth0)
	want := "fdea:894a:d11a:1:be24:11ff:fe93:59e7" // the stable ULA, preferred over the stable GUA
	if got != want {
		t.Fatalf("pickStableIPv6(real host data) = %q, want %q", got, want)
	}
	for _, temporary := range []string{
		"2603:8002:7900:22f1:7848:c633:b5de:d169", // temporary GUA
		"fdea:894a:d11a:1:6669:5e4:94a1:ab7e",     // temporary ULA
	} {
		if got == temporary {
			t.Fatalf("pickStableIPv6 returned a temporary address (flags=00, indistinguishable from its stable sibling by flag alone): %s", got)
		}
	}
}

func TestPickStableIPv6_PrefersULAOverGUAEvenWhenGUAComesFirstInFile(t *testing.T) {
	// Same two stable addresses as the real-host fixture, GUA deliberately
	// listed first, with no other lines to prove the outcome is preferULA's
	// doing and not an accident of exclusion filtering elsewhere.
	data := `26038002790022f1be2411fffe9359e7 02 40 00 00     eth0
fdea894ad11a0001be2411fffe9359e7 02 40 00 00     eth0
`
	got := pickStableIPv6(parseIfInet6(data), macOfEth0)
	want := "fdea:894a:d11a:1:be24:11ff:fe93:59e7"
	if got != want {
		t.Fatalf("pickStableIPv6 = %q, want the ULA %q preferred over the GUA that came first in file order", got, want)
	}
}

func TestPickStableIPv6_ULATiebreakAppliesToPermanentTierToo(t *testing.T) {
	// Two IFA_F_PERMANENT addresses (tier 1), GUA first in file order --
	// the ULA must still win.
	data := `20010db8deadbeef0000000000000001 02 40 00 80     eth0
fdaabbccddee00010000000000000001 02 40 00 80     eth0
`
	got := pickStableIPv6(parseIfInet6(data), macOfEth0)
	want := "fdaa:bbcc:ddee:1::1"
	if got != want {
		t.Fatalf("pickStableIPv6 = %q, want the permanent ULA %q preferred over the permanent GUA", got, want)
	}
}

func TestPickStableIPv6_FallsBackToGUAWhenNoULAQualifies(t *testing.T) {
	// If the only qualifying address is a GUA, preferULA must still return
	// it rather than an empty result.
	data := `26038002790022f1be2411fffe9359e7 02 40 00 00     eth0
`
	got := pickStableIPv6(parseIfInet6(data), macOfEth0)
	want := "2603:8002:7900:22f1:be24:11ff:fe93:59e7"
	if got != want {
		t.Fatalf("pickStableIPv6 = %q, want the only available address %q", got, want)
	}
}

func TestIsULA(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"fd00::1", true},
		{"fdea:894a:d11a:1:be24:11ff:fe93:59e7", true},
		{"fc00::1", false}, // reserved half of fc00::/7, not used in practice -- not treated as ULA here
		{"2603:8002:7900:22f1::1", false},
		{"fe80::1", false},
		{"::1", false},
	}
	for _, c := range cases {
		got := isULA(net.ParseIP(c.ip))
		if got != c.want {
			t.Errorf("isULA(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}
