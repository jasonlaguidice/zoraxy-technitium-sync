// Package reconciler computes and applies the difference between the
// hostnames Zoraxy currently wants routed and the DNS records Technitium
// currently has
package reconciler

import (
	"context"
	"fmt"
	"strings"

	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/svcparam"
)

// HostLister returns the hostnames that should have an A (and optionally
// AAAA/HTTPS) record, derived from Zoraxy's enabled proxy host rules.
type HostLister interface {
	ListDesiredHosts(ctx context.Context) ([]string, error)
}

// HTTPSSpec describes one HTTPS (RFC 9460) record, either as desired (built
// from the plugin's config) or as found in the zone. Priority 0 means alias
// mode; anything above is service mode. TargetName is the record's target
// domain name, with "" and "." meaning the same thing (the zone root).
// Params are the service binding parameters; AutoIPv4Hint/AutoIPv6Hint
// mirror Technitium's Automatic Hints option, which makes Technitium resolve
// and maintain the ipv4hint/ipv6hint params from the target name's A/AAAA
// records itself.
type HTTPSSpec struct {
	Priority     int
	TargetName   string
	Params       []svcparam.Param
	AutoIPv4Hint bool
	AutoIPv6Hint bool
}

// HostRecords is one hostname's current state in the managed DNS zone.
type HostRecords struct {
	AIPs       []string
	AAAAIPs    []string
	HTTPSSpecs []HTTPSSpec
	OwnedByUs  bool
}

// ZoneState is a snapshot of every hostname in the zone that currently has
// an A/AAAA/HTTPS record and/or our ownership TXT marker.
type ZoneState struct {
	Hosts map[string]HostRecords
}

// DNSStore reads the current zone state and applies the individual mutations
// the reconciler decides on. Implementations are expected to re-verify
// ownership immediately before Update*/Delete* mutations as a safety net
// (see internal/technitium), independent of the snapshot ownership already
// checked here.
type DNSStore interface {
	GetZoneState(ctx context.Context) (ZoneState, error)

	// CreateHost adds a fresh A record (and the ownership marker), plus an
	// AAAA record if ipv6 is non-empty and an HTTPS record if https is
	// non-empty (at most one is supported). Used both for brand-new
	// hostnames and for the rare case where we own the marker but the A
	// record is missing.
	CreateHost(ctx context.Context, hostname, ipv4, ipv6 string, https []HTTPSSpec) error
	UpdateA(ctx context.Context, hostname, oldIP, newIP string) error
	CreateAAAA(ctx context.Context, hostname, ip string) error
	UpdateAAAA(ctx context.Context, hostname, oldIP, newIP string) error
	// DeleteAAAAOnly removes just the AAAA record (used when the AAAA toggle
	// is switched off), leaving the A record and ownership marker intact.
	DeleteAAAAOnly(ctx context.Context, hostname, ip string) error
	// CreateHTTPS adds the desired HTTPS record for an owned hostname.
	CreateHTTPS(ctx context.Context, hostname string, spec HTTPSSpec) error
	// UpdateHTTPS rewrites the existing HTTPS record (identified by current,
	// exactly as Technitium requires for SVCB-family updates) into desired.
	UpdateHTTPS(ctx context.Context, hostname string, current, desired HTTPSSpec) error
	// DeleteHTTPSOnly removes just the HTTPS record (used when the HTTPS
	// toggle is switched off), leaving the other records and the ownership
	// marker intact.
	DeleteHTTPSOnly(ctx context.Context, hostname string, current HTTPSSpec) error
	// DeleteHost removes the A record, AAAA record (if ip6 is non-empty), the
	// first HTTPS record (if https is non-empty) and the ownership marker.
	DeleteHost(ctx context.Context, hostname, ip4, ip6 string, https []HTTPSSpec) error
}

// Options are the per-cycle desired-state parameters, sourced from the
// plugin's own config and expected to change at runtime as the user edits it.
type Options struct {
	IPv4Target  string
	AAAAEnabled bool
	IPv6Target  string

	HTTPSEnabled bool
	HTTPS        HTTPSSpec
}

// Result summarises one reconcile cycle for logging and the status UI.
type Result struct {
	Created          []string
	Updated          []string
	Deleted          []string
	Skipped          []string
	DeletionsSkipped bool
	Errors           []string
	ManagedCount     int
}

// Reconciler ties a HostLister and a DNSStore together and applies the
// circuit breaker across cycles. It is stateful only in lastGoodHostCount, so
// callers must keep one instance alive for the whole run rather than
// constructing a fresh one per cycle.
type Reconciler struct {
	Hosts HostLister
	Store DNSStore
	Opts  Options

	lastGoodHostCount int
}

func New(hosts HostLister, store DNSStore, opts Options) *Reconciler {
	return &Reconciler{Hosts: hosts, Store: store, Opts: opts}
}

func normalize(hostname string) string {
	return strings.ToLower(strings.TrimSpace(hostname))
}

// needsUpdate reports whether ips isn't exactly the single desired address.
func needsUpdate(ips []string, target string) bool {
	return len(ips) != 1 || ips[0] != target
}

func firstOrEmpty(ips []string) string {
	if len(ips) == 0 {
		return ""
	}
	return ips[0]
}

// canonicalHTTPSName canonicalizes an SVCB target name the way Technitium
// does (case-insensitive, surrounding whitespace and dot trimming), so that
// "App.Example.com.", "." and "" all compare equal.
func canonicalHTTPSName(name string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(name), "."))
}

// effectiveParams drops the params Technitium manages automatically from one
// side of a comparison: when Automatic Hints are on for ipv4hint (or
// ipv6hint), the param's stored value is whatever Technitium last resolved
// from the zone, so it must not be compared against the configured value -
// the auto flag itself is what is compared instead.
func effectiveParams(spec HTTPSSpec) []svcparam.Param {
	out := make([]svcparam.Param, 0, len(spec.Params))
	for _, p := range spec.Params {
		k, _ := svcparam.NormalizeKey(p.Key)
		if spec.AutoIPv4Hint && k == "ipv4hint" {
			continue
		}
		if spec.AutoIPv6Hint && k == "ipv6hint" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// sameHTTPS reports whether the zone's record already matches the desired
// spec. Manual hint params are still sent to Technitium alongside the auto
// flags (Technitium overwrites them with the zone-resolved value), so they
// are excluded from the comparison via effectiveParams.
func sameHTTPS(actual, desired HTTPSSpec) bool {
	if actual.Priority != desired.Priority {
		return false
	}
	if actual.AutoIPv4Hint != desired.AutoIPv4Hint || actual.AutoIPv6Hint != desired.AutoIPv6Hint {
		return false
	}
	if canonicalHTTPSName(actual.TargetName) != canonicalHTTPSName(desired.TargetName) {
		return false
	}
	return svcparam.CanonicalParams(effectiveParams(actual)) == svcparam.CanonicalParams(effectiveParams(desired))
}

// Reconcile runs one full cycle: fetch desired hosts from Zoraxy, fetch
// actual zone state from Technitium, apply the circuit breaker, then create,
// update and (if not tripped) delete records to close the gap.
func (r *Reconciler) Reconcile(ctx context.Context) (Result, error) {
	var res Result

	desired, err := r.Hosts.ListDesiredHosts(ctx)
	if err != nil {
		return res, fmt.Errorf("listing desired hosts from zoraxy: %w", err)
	}

	desiredSet := make(map[string]bool, len(desired))
	for _, h := range desired {
		if n := normalize(h); n != "" {
			desiredSet[n] = true
		}
	}
	currentCount := len(desiredSet)

	previousGoodCount := r.lastGoodHostCount
	skipDeletes := previousGoodCount > 0 && currentCount*2 < previousGoodCount
	r.lastGoodHostCount = currentCount
	res.DeletionsSkipped = skipDeletes

	zone, err := r.Store.GetZoneState(ctx)
	if err != nil {
		return res, fmt.Errorf("reading zone state from technitium: %w", err)
	}
	if zone.Hosts == nil {
		zone.Hosts = map[string]HostRecords{}
	}

	for hostname := range desiredSet {
		hr := zone.Hosts[hostname]

		// Any existing A, AAAA or HTTPS record with no matching ownership
		// marker is foreign, even if only one record type is populated (e.g.
		// someone manually created an AAAA-only record) - it must never be
		// adopted by falling through to the create path below.
		if !hr.OwnedByUs && (len(hr.AIPs) > 0 || len(hr.AAAAIPs) > 0 || len(hr.HTTPSSpecs) > 0) {
			res.Skipped = append(res.Skipped, hostname)
			continue
		}

		changed := false

		if len(hr.AIPs) == 0 {
			ipv6 := ""
			if r.Opts.AAAAEnabled {
				ipv6 = r.Opts.IPv6Target
			}
			https := []HTTPSSpec(nil)
			if r.Opts.HTTPSEnabled && len(hr.HTTPSSpecs) == 0 {
				https = []HTTPSSpec{r.Opts.HTTPS}
			}
			if err := r.Store.CreateHost(ctx, hostname, r.Opts.IPv4Target, ipv6, https); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("create %s: %v", hostname, err))
				continue
			}
			res.Created = append(res.Created, hostname)
			res.ManagedCount++
			continue
		}

		// Existing HTTPS records are updated or removed before any A/AAAA
		// mutation. Those mutations are sent with updateSvcbHints=true, which
		// makes Technitium rewrite the hint params of the zone's HTTPS
		// records; Technitium identifies the record to update or delete by its
		// current params, so doing this afterwards would use a stale snapshot.
		if r.Opts.HTTPSEnabled {
			if len(hr.HTTPSSpecs) > 0 {
				if first := hr.HTTPSSpecs[0]; !sameHTTPS(first, r.Opts.HTTPS) {
					if err := r.Store.UpdateHTTPS(ctx, hostname, first, r.Opts.HTTPS); err != nil {
						res.Errors = append(res.Errors, fmt.Sprintf("update HTTPS %s: %v", hostname, err))
					} else {
						changed = true
					}
				}
			}
		} else if len(hr.HTTPSSpecs) > 0 {
			if err := r.Store.DeleteHTTPSOnly(ctx, hostname, hr.HTTPSSpecs[0]); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("remove HTTPS %s: %v", hostname, err))
			} else {
				changed = true
			}
		}

		if needsUpdate(hr.AIPs, r.Opts.IPv4Target) {
			if err := r.Store.UpdateA(ctx, hostname, hr.AIPs[0], r.Opts.IPv4Target); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("update A %s: %v", hostname, err))
			} else {
				changed = true
			}
		}

		if r.Opts.AAAAEnabled {
			if len(hr.AAAAIPs) == 0 {
				if err := r.Store.CreateAAAA(ctx, hostname, r.Opts.IPv6Target); err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("create AAAA %s: %v", hostname, err))
				} else {
					changed = true
				}
			} else if needsUpdate(hr.AAAAIPs, r.Opts.IPv6Target) {
				if err := r.Store.UpdateAAAA(ctx, hostname, hr.AAAAIPs[0], r.Opts.IPv6Target); err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("update AAAA %s: %v", hostname, err))
				} else {
					changed = true
				}
			}
		} else if len(hr.AAAAIPs) > 0 {
			if err := r.Store.DeleteAAAAOnly(ctx, hostname, hr.AAAAIPs[0]); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("remove AAAA %s: %v", hostname, err))
			} else {
				changed = true
			}
		}

		// Creating the HTTPS record last lets Technitium resolve its automatic
		// hints from the A/AAAA records as they now stand.
		if r.Opts.HTTPSEnabled && len(hr.HTTPSSpecs) == 0 {
			if err := r.Store.CreateHTTPS(ctx, hostname, r.Opts.HTTPS); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("create HTTPS %s: %v", hostname, err))
			} else {
				changed = true
			}
		}

		if changed {
			res.Updated = append(res.Updated, hostname)
		}
		res.ManagedCount++
	}

	if skipDeletes {
		return res, nil
	}

	for hostname, hr := range zone.Hosts {
		if !hr.OwnedByUs || desiredSet[hostname] {
			continue
		}
		https := []HTTPSSpec(nil)
		if len(hr.HTTPSSpecs) > 0 {
			https = hr.HTTPSSpecs[:1]
		}
		if err := r.Store.DeleteHost(ctx, hostname, firstOrEmpty(hr.AIPs), firstOrEmpty(hr.AAAAIPs), https); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("delete %s: %v", hostname, err))
			continue
		}
		res.Deleted = append(res.Deleted, hostname)
	}

	return res, nil
}
