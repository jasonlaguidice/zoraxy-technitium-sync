package main

import "testing"

// Zoraxy's plugin-store update-check compares VersionMajor/Minor/Patch
// against what it already has installed, so a version string that fails to
// parse must degrade to something obviously wrong (0.0.0) rather than
// panic plugin startup or silently keep whatever zero values Go gives ints
// anyway — this test exists so that "silently keep the zero value" and
// "deliberately fall back to 0,0,0" can't be confused for each other later.
func TestParseVersion(t *testing.T) {
	cases := []struct {
		in                  string
		major, minor, patch int
	}{
		{"1.2.3", 1, 2, 3},
		{"0.0.0", 0, 0, 0},
		{"10.20.30", 10, 20, 30},
		{"0.0.1", 0, 0, 1},
		// Malformed input must fall back to 0,0,0, not error or panic.
		{"", 0, 0, 0},
		{"1.2", 0, 0, 0},
		{"1.2.3.4", 0, 0, 0},
		{"a.b.c", 0, 0, 0},
		{"1.2.x", 0, 0, 0},
		{"v1.2.3", 0, 0, 0},
	}
	for _, c := range cases {
		major, minor, patch := parseVersion(c.in)
		if major != c.major || minor != c.minor || patch != c.patch {
			t.Errorf("parseVersion(%q) = %d,%d,%d, want %d,%d,%d",
				c.in, major, minor, patch, c.major, c.minor, c.patch)
		}
	}
}
