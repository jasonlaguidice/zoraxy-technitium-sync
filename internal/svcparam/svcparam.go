// Package svcparam models Technitium's "service binding" parameter list (the
// svcParams of SVCB/HTTPS records, RFC 9460). A param is one key/value pair;
// keys are either one of the well-known RFC key names or, for keys Technitium
// itself doesn't know, the raw numeric key code - exactly the two shapes
// Technitium's own HTTP API accepts for svcParams.
//
// This package is the single authority for how such keys and values are
// validated, compared (via canonical forms) and encoded into the pipe-
// separated strings Technitium's add/update/delete endpoints expect.
package svcparam

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// KnownKeys are the RFC 9460 service parameter key names Technitium's web
// console offers, in their canonical lowercase, dash-separated spelling (the
// same spelling Technitium's records/get response uses for svcParams keys).
var KnownKeys = []string{
	"mandatory",
	"alpn",
	"no-default-alpn",
	"port",
	"ipv4hint",
	"ipv6hint",
	"dohpath",
}

// Param is one service binding parameter: a key (a KnownKeys entry or a
// numeric key code for unknown keys) and its value.
type Param struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// IsKnownKey reports whether the canonical key is one of the RFC key names
// (anything else is a numeric key code for an unknown key).
func IsKnownKey(canonicalKey string) bool {
	for _, k := range KnownKeys {
		if k == canonicalKey {
			return true
		}
	}
	return false
}

// NormalizeKey canonicalizes a param key as entered by a user (or stored in
// the config file): case-insensitive, '_' and '-' are interchangeable, and
// numeric key codes are normalized to plain decimal. The second return is
// false for anything that is neither a known key nor a 0-65535 number.
func NormalizeKey(key string) (string, bool) {
	k := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(key, "_", "-")))
	if k == "" {
		return "", false
	}
	if IsKnownKey(k) {
		return k, true
	}
	if n, err := strconv.ParseUint(k, 10, 16); err == nil {
		return strconv.FormatUint(n, 10), true
	}
	return "", false
}

// ValidateParam checks one key/value pair the way Technitium's SVCB/HTTPS
// add/update/delete endpoints would parse it. The '|' character is rejected
// in both key and value because it is the pair separator of the encoded
// svcParams string.
func ValidateParam(key, value string) error {
	canonical, ok := NormalizeKey(key)
	if !ok {
		return fmt.Errorf("unknown HTTPS param key %q: use one of the RFC key names or a numeric key code", key)
	}
	if strings.Contains(value, "|") {
		return fmt.Errorf("HTTPS param %s: value must not contain the '|' character", canonical)
	}

	switch canonical {
	case "mandatory":
		// The value is a comma-separated list of the other param keys that
		// must be present (e.g. "alpn" or "alpn,port").
		entries := splitTrim(value, ",")
		if len(entries) == 0 {
			return errors.New("HTTPS param mandatory: value must list at least one key")
		}
		for _, entry := range entries {
			if _, ok := NormalizeKey(entry); !ok {
				return fmt.Errorf("HTTPS param mandatory: unknown key %q in value", entry)
			}
		}
	case "alpn":
		// A comma-separated list of ALPN protocol IDs (e.g. "h2,h3").
		entries := splitTrim(value, ",")
		if len(entries) == 0 {
			return errors.New("HTTPS param alpn: value must list at least one ALPN ID")
		}
	case "no-default-alpn":
		// The key's presence is what matters; Technitium ignores any value.
	case "port":
		if _, err := strconv.ParseUint(strings.TrimSpace(value), 10, 16); err != nil {
			return fmt.Errorf("HTTPS param port: value must be a port number (0-65535): %w", err)
		}
	case "ipv4hint":
		if err := validateIPHint(value, false); err != nil {
			return fmt.Errorf("HTTPS param ipv4hint: %w", err)
		}
	case "ipv6hint":
		if err := validateIPHint(value, true); err != nil {
			return fmt.Errorf("HTTPS param ipv6hint: %w", err)
		}
	case "dohpath":
		// Any string; Technitium stores it verbatim.
	default:
		// A numeric key code for a key Technitium doesn't know: Technitium
		// stores the value as raw bytes, given as a hex string (plain or
		// colon-separated pairs).
		if err := validateHex(value); err != nil {
			return fmt.Errorf("HTTPS param %s (unknown key): value must be a hex string: %w", canonical, err)
		}
	}
	return nil
}

// splitTrim splits value on sep, trims each entry and drops empty ones.
func splitTrim(value, sep string) []string {
	var entries []string
	for _, e := range strings.Split(value, sep) {
		if e = strings.TrimSpace(e); e != "" {
			entries = append(entries, e)
		}
	}
	return entries
}

func validateIPHint(value string, ipv6 bool) error {
	entries := splitTrim(value, ",")
	if len(entries) == 0 {
		if ipv6 {
			return errors.New("value must list at least one IPv6 address")
		}
		return errors.New("value must list at least one IPv4 address")
	}
	for _, entry := range entries {
		ip := net.ParseIP(entry)
		if ip == nil || ipv6 == (ip.To4() != nil) {
			kind := "IPv4"
			if ipv6 {
				kind = "IPv6"
			}
			return fmt.Errorf("%q is not a valid %s address", entry, kind)
		}
	}
	return nil
}

func validateHex(value string) error {
	hex := strings.ReplaceAll(strings.TrimSpace(value), ":", "")
	if len(hex)%2 != 0 {
		return errors.New("must be an even-length hex string")
	}
	for _, r := range hex {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return errors.New("contains a non-hex character")
		}
	}
	return nil
}

// CanonicalValue canonicalizes a param value for equality comparison: two
// values whose canonical forms are equal represent the same wire value.
func CanonicalValue(canonicalKey, value string) string {
	switch canonicalKey {
	case "mandatory":
		seen := map[string]bool{}
		var entries []string
		for _, e := range splitTrim(value, ",") {
			if k, ok := NormalizeKey(e); ok {
				e = k
			}
			if seen[e] {
				continue
			}
			seen[e] = true
			entries = append(entries, e)
		}
		sort.Strings(entries)
		return strings.Join(entries, ",")
	case "alpn":
		// ALPN order is significant (it is the preference order); keep it.
		return strings.Join(splitTrim(value, ","), ",")
	case "no-default-alpn":
		return ""
	case "port":
		if n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 16); err == nil {
			return strconv.FormatUint(n, 10)
		}
		return strings.TrimSpace(value)
	case "ipv4hint", "ipv6hint":
		var entries []string
		for _, e := range splitTrim(value, ",") {
			if ip := net.ParseIP(e); ip != nil {
				e = ip.String()
			}
			entries = append(entries, e)
		}
		return strings.Join(entries, ",")
	case "dohpath":
		return strings.TrimSpace(value)
	default:
		// Unknown key: Technitium reports the value as colon-separated hex.
		return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), ":", ""))
	}
}

// CanonicalParams builds a canonical, order-independent signature of a param
// list: keys are normalized and deduplicated (last one wins) and the result
// is sorted, so two lists that differ only in ordering or spelling compare
// equal. Duplicate keys are rejected by ValidateParam for user input, but the
// canonical form is also used on server-reported values, so it stays
// defensive here.
func CanonicalParams(params []Param) string {
	type entry struct {
		key   string
		value string
	}
	seen := map[string]string{}
	for _, p := range params {
		k, ok := NormalizeKey(p.Key)
		if !ok {
			continue
		}
		seen[k] = CanonicalValue(k, p.Value)
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(";")
		}
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(seen[k])
	}
	return sb.String()
}

// EncodeParams joins params into the pipe-separated svcParams string
// Technitium's add/update/delete endpoints expect, e.g.
// "alpn|h2,h3|port|53443". Values are sent verbatim (they round-trip through
// Technitium's own parser). The second return is false when there are no
// params, in which case the caller must send the literal "false" instead -
// that is how Technitium spells an empty parameter list.
func EncodeParams(params []Param) (string, bool) {
	if len(params) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(params)*2)
	for _, p := range params {
		k, ok := NormalizeKey(p.Key)
		if !ok {
			return "", false
		}
		parts = append(parts, k, p.Value)
	}
	return strings.Join(parts, "|"), true
}
