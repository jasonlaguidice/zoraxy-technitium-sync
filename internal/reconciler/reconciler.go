// Package reconciler computes and applies the difference between the
// hostnames Zoraxy currently wants routed and the DNS records Technitium
// currently has, without knowing anything about HTTP, Zoraxy's API shape or
// Technitium's API shape. It depends only on the two small interfaces below,
// so it is fully testable with in-memory fakes.
package reconciler

import (
	"context"
	"fmt"
	"strings"
)

// HostLister returns the hostnames that should have an A (and optionally
// AAAA) record, derived from Zoraxy's enabled proxy host rules.
type HostLister interface {
	ListDesiredHosts(ctx context.Context) ([]string, error)
}

// HostRecords is one hostname's current state in the managed DNS zone.
type HostRecords struct {
	AIPs      []string
	AAAAIPs   []string
	OwnedByUs bool
}

// ZoneState is a snapshot of every hostname in the zone that currently has
// an A/AAAA record and/or our ownership TXT marker.
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
	// AAAA record if ipv6 is non-empty. Used both for brand-new hostnames and
	// for the rare case where we own the marker but the A record is missing.
	CreateHost(ctx context.Context, hostname, ipv4, ipv6 string) error
	UpdateA(ctx context.Context, hostname, oldIP, newIP string) error
	CreateAAAA(ctx context.Context, hostname, ip string) error
	UpdateAAAA(ctx context.Context, hostname, oldIP, newIP string) error
	// DeleteAAAAOnly removes just the AAAA record (used when the AAAA toggle
	// is switched off), leaving the A record and ownership marker intact.
	DeleteAAAAOnly(ctx context.Context, hostname, ip string) error
	// DeleteHost removes the A record, AAAA record (if ip6 is non-empty) and
	// the ownership marker.
	DeleteHost(ctx context.Context, hostname, ip4, ip6 string) error
}

// Options are the per-cycle desired-state parameters, sourced from the
// plugin's own config and expected to change at runtime as the user edits it.
type Options struct {
	IPv4Target  string
	AAAAEnabled bool
	IPv6Target  string
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
	skipDeletes := false
	if currentCount == 0 {
		skipDeletes = true
	} else if previousGoodCount > 0 && currentCount*2 < previousGoodCount {
		skipDeletes = true
	}
	// Always advance the baseline to the count just observed, even when this
	// cycle tripped the breaker. Otherwise a genuine, permanent drop in host
	// count (not a transient glitch) would keep comparing every future cycle
	// against the stale pre-drop baseline and disable deletions forever
	// instead of just for the one cycle the breaker is meant to cover: the
	// next cycle's comparison is against the count we just saw, so it only
	// trips again if the count drops by half a second time in a row.
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

		// Any existing A or AAAA record with no matching ownership marker is
		// foreign, even if only the AAAA side is populated (e.g. someone
		// manually created an AAAA-only record) - it must never be adopted
		// by falling through to the create path below.
		if !hr.OwnedByUs && (len(hr.AIPs) > 0 || len(hr.AAAAIPs) > 0) {
			res.Skipped = append(res.Skipped, hostname)
			continue
		}

		changed := false

		if len(hr.AIPs) == 0 {
			ipv6 := ""
			if r.Opts.AAAAEnabled {
				ipv6 = r.Opts.IPv6Target
			}
			if err := r.Store.CreateHost(ctx, hostname, r.Opts.IPv4Target, ipv6); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("create %s: %v", hostname, err))
				continue
			}
			res.Created = append(res.Created, hostname)
			res.ManagedCount++
			continue
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
		if err := r.Store.DeleteHost(ctx, hostname, firstOrEmpty(hr.AIPs), firstOrEmpty(hr.AAAAIPs)); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("delete %s: %v", hostname, err))
			continue
		}
		res.Deleted = append(res.Deleted, hostname)
	}

	return res, nil
}
