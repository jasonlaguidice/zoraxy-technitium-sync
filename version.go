package main

import (
	"strconv"
	"strings"
)

// version is the plugin's release version, in "X.Y.Z" form. It defaults to
// "0.0.0" for any build that doesn't inject it — a plain `go build .` or
// `go run .` during local development — and is overridden at build time by
// CI via `-ldflags "-X main.version=<tag-without-the-v>"`, so the value
// baked into a released binary always matches the git tag it was built
// from, with nothing left for a human to remember to keep in sync.
//
// Zoraxy's plugin-store update-check relies on IntroSpect's
// VersionMajor/Minor/Patch fields (see main.go), which is why this needs to
// be real rather than decorative.
var version = "0.0.0"

// parseVersion parses a "X.Y.Z" string into three ints. It falls back to
// 0, 0, 0 on anything that doesn't parse cleanly, rather than erroring or
// panicking — a malformed build-time -ldflags value should degrade to an
// obviously-wrong version, not crash plugin startup.
func parseVersion(v string) (major, minor, patch int) {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return 0, 0, 0
	}
	ints := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, 0, 0
		}
		ints[i] = n
	}
	return ints[0], ints[1], ints[2]
}
