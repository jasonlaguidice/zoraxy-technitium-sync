//go:build !linux

package config

// detectLocalIPv6 on non-Linux platforms falls back to the same
// outbound-routing heuristic used for IPv4 (detectLocalIP): Linux is the
// only OS this handles properly, by reading /proc/net/if_inet6 to tell a
// stable address apart from a rotating RFC 4941 privacy address (see
// detect_ipv6_linux.go) -- there is no equivalent of that file elsewhere.
// Accepting the UDP-dial trick's limitation here (it may return a temporary
// address on a host that has one) is preferable to not detecting anything
// at all on the non-Linux binaries this plugin also ships.
func detectLocalIPv6() string {
	return detectLocalIP("udp6", "[2001:4860:4860::8888]:80")
}
