//go:build linux

package config

import (
	"bytes"
	"net"
	"os"
	"strconv"
	"strings"
)

// On Linux, the generic UDP-dial trick (detectLocalIP, used as-is for IPv4)
// is the wrong tool for IPv6: it returns whatever address the kernel's
// outbound route selection prefers, which by default is a temporary/privacy
// address (RFC 4941) that rotates on a timer specifically so it can't be
// correlated across connections. That is exactly the opposite of what a
// DNS AAAA record needs. So on Linux, IPv6 detection is done properly by
// reading /proc/net/if_inet6 and picking a stable address, in this order:
//
//  1. A permanently-configured address (DHCPv6-assigned or static) --
//     IFA_F_PERMANENT.
//  2. A stable SLAAC address: one whose interface identifier (low 64 bits)
//     is the EUI-64 derived from the owning interface's own MAC address --
//     never a temporary/privacy SLAAC address.
//  3. Otherwise "", same as if nothing were auto-detected at all.
//
// Within whichever tier actually has a match, a Unique Local Address
// (fd00::/8, RFC 4193 -- the IPv6 analog of an RFC 1918 private address, and
// the same spirit as LANIPv4 being a private LAN target elsewhere in this
// config) is preferred over a globally-routable one, since this field is a
// LAN target rather than a public-facing address. See preferULA.
//
// Flag values are from include/uapi/linux/if_addr.h in the Linux kernel
// source (stable UAPI, unchanged in over a decade, and the same constants
// iproute2 itself decodes these bytes against). The /proc/net/if_inet6 line
// format itself (address / ifindex / prefix length / scope / flags /
// device, all but the address and device in hex) is produced by
// if6_seq_show() in net/ipv6/addrconf.c.
//
// IMPORTANT, confirmed empirically against a real Debian 13 host's
// /proc/net/if_inet6: the temporary/deprecated/tentative/dad-failed flags
// below are NOT a reliable signal in practice. On that host, every
// global-scope address -- both the stable EUI-64 one AND its
// temporary/privacy sibling, for both its ULA and GUA prefixes -- reported
// flags=00. Only loopback and the link-local address carried any flag bit
// at all (0x80, presumably IFA_F_PERMANENT, since neither is SLAAC-derived).
// So the flag-based exclusion below is a best-effort bonus that may do
// nothing at all on a real system: the EUI-64-vs-MAC comparison in tier 2 is
// the load-bearing check that actually distinguishes a stable address from
// a temporary one. Do not assume the flags will catch what the EUI-64
// comparison misses.
const (
	ifaFTemporary  = 0x01 // aka IFA_F_SECONDARY -- same bit, reused meaning for IPv6 privacy addresses
	ifaFDADFailed  = 0x08
	ifaFDeprecated = 0x20
	ifaFTentative  = 0x40
	ifaFPermanent  = 0x80

	// notStableEnough is every flag that would disqualify an address
	// regardless of tier, if the kernel actually sets it: still going
	// through DAD, already failed DAD, on its way out, or a rotating
	// privacy address. Harmless to keep even though it's not guaranteed to
	// fire (see the note above) -- it costs nothing and may still help on
	// some kernel/config combination.
	notStableEnough = ifaFTemporary | ifaFDADFailed | ifaFDeprecated | ifaFTentative

	// scopeGlobal is the scope byte if6_seq_show writes for a global-scope
	// address. This is ifp->scope, i.e. ipv6_addr_type() masked to its
	// scope bits -- NOT the raw RFC 4007 scope number -- so the values seen
	// in this file are 0x00 global, 0x10 host/loopback, 0x20 link-local,
	// 0x40 site-local (deprecated), 0x80 compat (IPv4-compatible, unused
	// today). Only 0x00 is ever eligible here. Confirmed against the same
	// real host referenced above: global addresses showed 00, loopback 10,
	// link-local 20.
	scopeGlobal = 0x00
)

// ifInet6Addr is one parsed line of /proc/net/if_inet6.
type ifInet6Addr struct {
	ip      net.IP
	scope   uint8
	flags   uint8
	ifindex int
	device  string
}

// parseIfInet6 parses the contents of /proc/net/if_inet6. Lines that don't
// match the expected 6-field, 32-hex-char-address shape are skipped rather
// than treated as a fatal error -- a kernel that changes this format is a
// reason to fall back to "detected nothing", not to crash plugin startup.
func parseIfInet6(data string) []ifInet6Addr {
	var addrs []ifInet6Addr
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 6 {
			continue
		}
		hexAddr, ifindexHex, _, scopeHex, flagsHex, device := fields[0], fields[1], fields[2], fields[3], fields[4], fields[5]

		if len(hexAddr) != 32 {
			continue
		}
		ip := make(net.IP, net.IPv6len)
		ok := true
		for i := 0; i < net.IPv6len; i++ {
			b, err := strconv.ParseUint(hexAddr[i*2:i*2+2], 16, 8)
			if err != nil {
				ok = false
				break
			}
			ip[i] = byte(b)
		}
		if !ok {
			continue
		}

		ifindex, err := strconv.ParseInt(ifindexHex, 16, 64)
		if err != nil {
			continue
		}
		scope, err := strconv.ParseUint(scopeHex, 16, 8)
		if err != nil {
			continue
		}
		flags, err := strconv.ParseUint(flagsHex, 16, 8)
		if err != nil {
			continue
		}

		addrs = append(addrs, ifInet6Addr{
			ip:      ip,
			scope:   uint8(scope),
			flags:   uint8(flags),
			ifindex: int(ifindex),
			device:  device,
		})
	}
	return addrs
}

// eui64FromMAC derives the 8-byte interface identifier SLAAC would assign
// to a stable address on the interface with this MAC: flip the
// universal/local bit (0x02) of the first octet, then splice ff:fe into the
// middle of the 6-byte MAC to make it 8 bytes (RFC 4291 appendix A).
func eui64FromMAC(mac net.HardwareAddr) ([8]byte, bool) {
	if len(mac) != 6 {
		return [8]byte{}, false
	}
	return [8]byte{
		mac[0] ^ 0x02,
		mac[1],
		mac[2],
		0xff,
		0xfe,
		mac[3],
		mac[4],
		mac[5],
	}, true
}

// pickStableIPv6 applies the priority order documented above to a parsed
// address list. macOf looks up an interface's hardware address by its
// netlink ifindex (net.Interfaces(), abstracted behind a function argument
// so this selection logic can be tested without depending on the real
// network stack).
func pickStableIPv6(addrs []ifInet6Addr, macOf func(ifindex int) net.HardwareAddr) string {
	candidates := make([]ifInet6Addr, 0, len(addrs))
	for _, a := range addrs {
		if a.scope != scopeGlobal {
			continue // excludes link-local, loopback/host, site-local, compat
		}
		if a.ip.IsLoopback() || a.ip.IsLinkLocalUnicast() {
			continue // belt-and-suspenders beyond the scope byte
		}
		if a.flags&notStableEnough != 0 {
			continue // best-effort only -- see the note on notStableEnough
		}
		candidates = append(candidates, a)
	}

	// Tier 1: DHCPv6-assigned or statically-configured.
	var tier1 []ifInet6Addr
	for _, a := range candidates {
		if a.flags&ifaFPermanent != 0 {
			tier1 = append(tier1, a)
		}
	}
	if len(tier1) > 0 {
		return preferULA(tier1).ip.String()
	}

	// Tier 2: a stable (EUI-64) SLAAC address -- its interface identifier
	// must match the EUI-64 derived from the owning interface's own MAC.
	// This is the discriminator that actually does the work of telling a
	// stable address apart from its temporary/privacy sibling: on a real
	// host, both may report identical (zero) flags, so this comparison,
	// not the flags above, is what tier 2 actually rests on.
	var tier2 []ifInet6Addr
	for _, a := range candidates {
		mac := macOf(a.ifindex)
		id, ok := eui64FromMAC(mac)
		if !ok {
			continue
		}
		if bytes.Equal(a.ip[8:16], id[:]) {
			tier2 = append(tier2, a)
		}
	}
	if len(tier2) > 0 {
		return preferULA(tier2).ip.String()
	}

	return ""
}

// isULA reports whether ip is a Unique Local Address: RFC 4193's fc00::/7
// with the "locally assigned" (L) bit set, i.e. fd00::/8. fc00::/8 itself
// (L bit clear) is reserved and not used in practice, so fd00::/8 is what
// every real ULA prefix looks like -- the IPv6 analog of an RFC 1918
// private address.
func isULA(ip net.IP) bool {
	ip16 := ip.To16()
	return ip16 != nil && ip16[0] == 0xfd
}

// preferULA returns the first ULA address in addrs, or addrs[0] if none of
// them are ULA. addrs must be non-empty. This is a LAN target, not a
// public-facing one, so a private (ULA) address is preferred over a
// globally-routable one when both are equally qualified -- otherwise the
// choice would depend on /proc/net/if_inet6's own (arbitrary) line order.
func preferULA(addrs []ifInet6Addr) ifInet6Addr {
	for _, a := range addrs {
		if isULA(a.ip) {
			return a
		}
	}
	return addrs[0]
}

// interfaceMACByIndex looks up a live interface's hardware address by its
// ifindex, as reported by the kernel via net.Interfaces(). Returns nil
// (safely rejected by eui64FromMAC) if the index isn't found or the lookup
// itself fails.
func interfaceMACByIndex(ifindex int) net.HardwareAddr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Index == ifindex {
			return iface.HardwareAddr
		}
	}
	return nil
}

func detectLocalIPv6() string {
	data, err := os.ReadFile("/proc/net/if_inet6")
	if err != nil {
		// No /proc/net/if_inet6 (IPv6 disabled at the kernel level, or some
		// non-standard /proc mount) -- there is nothing to detect properly,
		// and falling back to the UDP-dial heuristic here would silently
		// reintroduce the exact privacy-address problem this file exists to
		// avoid. Blank is the honest answer.
		return ""
	}
	return pickStableIPv6(parseIfInet6(string(data)), interfaceMACByIndex)
}
