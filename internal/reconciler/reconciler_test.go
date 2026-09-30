package reconciler

import (
	"context"
	"testing"

	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/svcparam"
)

type fakeHostLister struct {
	hosts []string
	err   error
}

func (f *fakeHostLister) ListDesiredHosts(ctx context.Context) ([]string, error) {
	return f.hosts, f.err
}

type call struct {
	op       string
	hostname string
	a, b     string
	https    []HTTPSSpec
	spec     HTTPSSpec
}

type fakeStore struct {
	hosts map[string]HostRecords
	calls []call
	err   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{hosts: map[string]HostRecords{}}
}

func (f *fakeStore) GetZoneState(ctx context.Context) (ZoneState, error) {
	if f.err != nil {
		return ZoneState{}, f.err
	}
	cp := make(map[string]HostRecords, len(f.hosts))
	for k, v := range f.hosts {
		cp[k] = v
	}
	return ZoneState{Hosts: cp}, nil
}

func (f *fakeStore) CreateHost(ctx context.Context, hostname, ipv4, ipv6 string, https []HTTPSSpec) error {
	f.calls = append(f.calls, call{op: "create", hostname: hostname, a: ipv4, b: ipv6, https: https})
	hr := HostRecords{AIPs: []string{ipv4}, OwnedByUs: true}
	if ipv6 != "" {
		hr.AAAAIPs = []string{ipv6}
	}
	hr.HTTPSSpecs = https
	f.hosts[hostname] = hr
	return nil
}

func (f *fakeStore) UpdateA(ctx context.Context, hostname, oldIP, newIP string) error {
	f.calls = append(f.calls, call{op: "updateA", hostname: hostname, a: oldIP, b: newIP})
	hr := f.hosts[hostname]
	hr.AIPs = []string{newIP}
	f.hosts[hostname] = hr
	return nil
}

func (f *fakeStore) CreateAAAA(ctx context.Context, hostname, ip string) error {
	f.calls = append(f.calls, call{op: "createAAAA", hostname: hostname, a: ip})
	hr := f.hosts[hostname]
	hr.AAAAIPs = []string{ip}
	f.hosts[hostname] = hr
	return nil
}

func (f *fakeStore) UpdateAAAA(ctx context.Context, hostname, oldIP, newIP string) error {
	f.calls = append(f.calls, call{op: "updateAAAA", hostname: hostname, a: oldIP, b: newIP})
	hr := f.hosts[hostname]
	hr.AAAAIPs = []string{newIP}
	f.hosts[hostname] = hr
	return nil
}

func (f *fakeStore) DeleteAAAAOnly(ctx context.Context, hostname, ip string) error {
	f.calls = append(f.calls, call{op: "deleteAAAAOnly", hostname: hostname, a: ip})
	hr := f.hosts[hostname]
	hr.AAAAIPs = nil
	f.hosts[hostname] = hr
	return nil
}

func (f *fakeStore) CreateHTTPS(ctx context.Context, hostname string, spec HTTPSSpec) error {
	f.calls = append(f.calls, call{op: "createHTTPS", hostname: hostname, spec: spec})
	hr := f.hosts[hostname]
	hr.HTTPSSpecs = append(hr.HTTPSSpecs, spec)
	f.hosts[hostname] = hr
	return nil
}

func (f *fakeStore) UpdateHTTPS(ctx context.Context, hostname string, current, desired HTTPSSpec) error {
	f.calls = append(f.calls, call{op: "updateHTTPS", hostname: hostname, spec: desired})
	hr := f.hosts[hostname]
	if len(hr.HTTPSSpecs) > 0 {
		hr.HTTPSSpecs[0] = desired
	}
	f.hosts[hostname] = hr
	return nil
}

func (f *fakeStore) DeleteHTTPSOnly(ctx context.Context, hostname string, current HTTPSSpec) error {
	f.calls = append(f.calls, call{op: "deleteHTTPSOnly", hostname: hostname, spec: current})
	hr := f.hosts[hostname]
	hr.HTTPSSpecs = nil
	f.hosts[hostname] = hr
	return nil
}

func (f *fakeStore) DeleteHost(ctx context.Context, hostname, ip4, ip6 string, https []HTTPSSpec) error {
	f.calls = append(f.calls, call{op: "delete", hostname: hostname, a: ip4, b: ip6, https: https})
	delete(f.hosts, hostname)
	return nil
}

func hasCall(calls []call, op, hostname string) bool {
	for _, c := range calls {
		if c.op == op && c.hostname == hostname {
			return true
		}
	}
	return false
}

func TestReconcile_CreatesNewHost(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"new.example.com"}}
	store := newFakeStore()
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24"})

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Created) != 1 || res.Created[0] != "new.example.com" {
		t.Fatalf("expected new.example.com to be created, got %+v", res)
	}
	if !hasCall(store.calls, "create", "new.example.com") {
		t.Fatalf("expected CreateHost call, got %+v", store.calls)
	}
	if store.hosts["new.example.com"].AIPs[0] != "192.168.1.24" {
		t.Fatalf("unexpected A record: %+v", store.hosts["new.example.com"])
	}
}

func TestReconcile_UpdatesChangedIP(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"app.example.com"}}
	store := newFakeStore()
	store.hosts["app.example.com"] = HostRecords{AIPs: []string{"10.0.0.9"}, OwnedByUs: true}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24"})

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Updated) != 1 || res.Updated[0] != "app.example.com" {
		t.Fatalf("expected app.example.com to be updated, got %+v", res)
	}
	if !hasCall(store.calls, "updateA", "app.example.com") {
		t.Fatalf("expected UpdateA call, got %+v", store.calls)
	}
	if store.hosts["app.example.com"].AIPs[0] != "192.168.1.24" {
		t.Fatalf("IP was not updated: %+v", store.hosts["app.example.com"])
	}
}

func TestReconcile_DeletesRemovedHostWithMarker(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{}}
	store := newFakeStore()
	store.hosts["gone.example.com"] = HostRecords{AIPs: []string{"192.168.1.24"}, OwnedByUs: true}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24"})
	// Seed the circuit breaker baseline with a prior successful poll so this
	// cycle's zero count doesn't itself get treated as the trigger — use two
	// cycles instead: first a normal one, then the removal.
	r.lastGoodHostCount = 1

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// currentCount is 0, which trips the circuit breaker by design (spec:
	// zero hosts always skips deletes). To test an actual deletion we need a
	// nonzero desired set that simply no longer includes this host.
	if !res.DeletionsSkipped {
		t.Fatalf("expected zero-host poll to skip deletions")
	}
	if len(res.Deleted) != 0 {
		t.Fatalf("expected no deletions on a zero-host poll, got %+v", res.Deleted)
	}

	// Now do it properly: desired set is nonzero but doesn't mention the
	// removed host.
	hosts.hosts = []string{"other.example.com"}
	res, err = r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "gone.example.com" {
		t.Fatalf("expected gone.example.com to be deleted, got %+v", res)
	}
	if !hasCall(store.calls, "delete", "gone.example.com") {
		t.Fatalf("expected DeleteHost call, got %+v", store.calls)
	}
	if _, exists := store.hosts["gone.example.com"]; exists {
		t.Fatalf("expected gone.example.com to be removed from store")
	}
}

func TestReconcile_SkipsRemovedHostWithoutMarker(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"other.example.com"}}
	store := newFakeStore()
	store.hosts["foreign.example.com"] = HostRecords{AIPs: []string{"10.1.1.1"}, OwnedByUs: false}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24"})

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Deleted) != 0 {
		t.Fatalf("expected no deletions, got %+v", res.Deleted)
	}
	if hasCall(store.calls, "delete", "foreign.example.com") {
		t.Fatalf("must never delete a record without our ownership marker")
	}
	if _, exists := store.hosts["foreign.example.com"]; !exists {
		t.Fatalf("foreign record should still exist untouched")
	}
}

func TestReconcile_SkipsExistingForeignRecordOnCreatePath(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"foreign.example.com"}}
	store := newFakeStore()
	store.hosts["foreign.example.com"] = HostRecords{AIPs: []string{"10.1.1.1"}, OwnedByUs: false}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24"})

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "foreign.example.com" {
		t.Fatalf("expected foreign.example.com to be skipped, got %+v", res)
	}
	if hasCall(store.calls, "create", "foreign.example.com") || hasCall(store.calls, "updateA", "foreign.example.com") {
		t.Fatalf("must never touch a foreign record: %+v", store.calls)
	}
	if store.hosts["foreign.example.com"].AIPs[0] != "10.1.1.1" {
		t.Fatalf("foreign record was mutated: %+v", store.hosts["foreign.example.com"])
	}
}

func TestReconcile_SkipsForeignAAAAOnlyRecordInsteadOfAdopting(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"foreign.example.com"}}
	store := newFakeStore()
	// No A record at all, only a foreign AAAA record with no marker. This
	// must not be treated as "nothing exists yet, safe to create" - it has
	// to be skipped exactly like a foreign A record would be.
	store.hosts["foreign.example.com"] = HostRecords{AAAAIPs: []string{"fd00::9"}, OwnedByUs: false}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24", AAAAEnabled: true, IPv6Target: "fd00::1"})

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "foreign.example.com" {
		t.Fatalf("expected foreign.example.com to be skipped, got %+v", res)
	}
	if hasCall(store.calls, "create", "foreign.example.com") {
		t.Fatalf("must never adopt a foreign AAAA-only record by creating an A record and marker next to it: %+v", store.calls)
	}
	if len(store.hosts["foreign.example.com"].AIPs) != 0 {
		t.Fatalf("must not have added an A record to a foreign host: %+v", store.hosts["foreign.example.com"])
	}
	if store.hosts["foreign.example.com"].AAAAIPs[0] != "fd00::9" {
		t.Fatalf("foreign AAAA record was mutated: %+v", store.hosts["foreign.example.com"])
	}
}

func TestReconcile_CircuitBreakerSkipsDeletesButNotCreatesOrUpdates(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com"}}
	store := newFakeStore()
	for i, h := range hosts.hosts {
		ip := "192.168.1.24"
		if i == 0 {
			ip = "10.0.0.1" // will need an update
		}
		store.hosts[h] = HostRecords{AIPs: []string{ip}, OwnedByUs: true}
	}
	store.hosts["stale.example.com"] = HostRecords{AIPs: []string{"192.168.1.24"}, OwnedByUs: true}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24"})
	r.lastGoodHostCount = 5 // last successful poll saw 5 desired hosts

	// This poll only returns 2 hosts: fewer than 50% of 5, so it must skip
	// deletes but still create/update normally.
	hosts.hosts = []string{"a.example.com", "new.example.com"}
	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.DeletionsSkipped {
		t.Fatalf("expected circuit breaker to trip and skip deletions")
	}
	if len(res.Deleted) != 0 {
		t.Fatalf("expected no deletions while circuit breaker is tripped, got %+v", res.Deleted)
	}
	if _, exists := store.hosts["stale.example.com"]; !exists {
		t.Fatalf("stale.example.com should not have been deleted")
	}
	if !hasCall(store.calls, "updateA", "a.example.com") {
		t.Fatalf("expected update of a.example.com to still happen, got %+v", store.calls)
	}
	if !hasCall(store.calls, "create", "new.example.com") {
		t.Fatalf("expected create of new.example.com to still happen, got %+v", store.calls)
	}
}

// TestReconcile_CircuitBreakerSelfClearsAfterSustainedDrop verifies the
// breaker only ever covers a single cycle: once a lower host count repeats
// on the very next poll (a legitimate, sustained drop rather than a
// transient glitch), deletions must resume immediately rather than staying
// disabled forever because the baseline never came down.
func TestReconcile_CircuitBreakerSelfClearsAfterSustainedDrop(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com"}}
	store := newFakeStore()
	for _, h := range hosts.hosts {
		store.hosts[h] = HostRecords{AIPs: []string{"192.168.1.24"}, OwnedByUs: true}
	}
	store.hosts["stale.example.com"] = HostRecords{AIPs: []string{"192.168.1.24"}, OwnedByUs: true}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24"})
	r.lastGoodHostCount = 10 // baseline before the drop

	// Cycle 1: count drops from 10 to 4 (fewer than 50%) -> breaker trips.
	res1, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res1.DeletionsSkipped {
		t.Fatalf("expected the first cycle to trip the breaker")
	}
	if _, exists := store.hosts["stale.example.com"]; !exists {
		t.Fatalf("stale.example.com must survive the tripped cycle")
	}

	// Cycle 2: count stays at 4 (a real, sustained new steady state, not a
	// blip) - the breaker must self-clear now that 4 is the new baseline,
	// and the truly-removed stale host must finally be deleted.
	res2, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.DeletionsSkipped {
		t.Fatalf("expected the breaker to self-clear on the second consecutive cycle at the same count")
	}
	if len(res2.Deleted) != 1 || res2.Deleted[0] != "stale.example.com" {
		t.Fatalf("expected stale.example.com to finally be deleted, got %+v", res2)
	}
}

func TestReconcile_CircuitBreakerSelfClearsAfterDropToZero(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"only.example.com"}}
	store := newFakeStore()
	store.hosts["only.example.com"] = HostRecords{AIPs: []string{"192.168.1.24"}, OwnedByUs: true}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24"})
	r.lastGoodHostCount = 1

	// Cycle 1: the only host is removed, count drops to 0 -> breaker trips.
	hosts.hosts = nil
	res1, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res1.DeletionsSkipped {
		t.Fatalf("expected the first cycle at zero to trip the breaker")
	}
	if _, exists := store.hosts["only.example.com"]; !exists {
		t.Fatalf("only.example.com must survive the tripped cycle")
	}

	// Cycle 2: still zero - a real, sustained empty state, not a blip - the
	// breaker must self-clear and the removed host must finally be deleted.
	res2, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.DeletionsSkipped {
		t.Fatalf("expected the breaker to self-clear on the second consecutive cycle at zero")
	}
	if len(res2.Deleted) != 1 || res2.Deleted[0] != "only.example.com" {
		t.Fatalf("expected only.example.com to finally be deleted, got %+v", res2)
	}
}

func TestReconcile_AAAAEnabledCreatesAndUpdates(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com", "b.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{AIPs: []string{"192.168.1.24"}, OwnedByUs: true}
	store.hosts["b.example.com"] = HostRecords{AIPs: []string{"192.168.1.24"}, AAAAIPs: []string{"fd00::9"}, OwnedByUs: true}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24", AAAAEnabled: true, IPv6Target: "fd00::1"})

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasCall(store.calls, "createAAAA", "a.example.com") {
		t.Fatalf("expected AAAA to be created for a.example.com, got %+v", store.calls)
	}
	if !hasCall(store.calls, "updateAAAA", "b.example.com") {
		t.Fatalf("expected AAAA to be updated for b.example.com, got %+v", store.calls)
	}
	updatedSet := map[string]bool{}
	for _, h := range res.Updated {
		updatedSet[h] = true
	}
	if !updatedSet["a.example.com"] || !updatedSet["b.example.com"] {
		t.Fatalf("expected both hosts reported as updated, got %+v", res.Updated)
	}
}

func TestReconcile_AAAADisabledRemovesExistingAAAA(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{AIPs: []string{"192.168.1.24"}, AAAAIPs: []string{"fd00::1"}, OwnedByUs: true}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24", AAAAEnabled: false})

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasCall(store.calls, "deleteAAAAOnly", "a.example.com") {
		t.Fatalf("expected AAAA to be removed for a.example.com, got %+v", store.calls)
	}
	if len(store.hosts["a.example.com"].AIPs) == 0 {
		t.Fatalf("A record must survive an AAAA-only removal")
	}
	if len(res.Updated) != 1 {
		t.Fatalf("expected host reported as updated, got %+v", res.Updated)
	}
}

func TestReconcile_ErrorListingHostsIsSurfaced(t *testing.T) {
	hosts := &fakeHostLister{err: context.DeadlineExceeded}
	store := newFakeStore()
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24"})

	if _, err := r.Reconcile(context.Background()); err == nil {
		t.Fatalf("expected error to be surfaced")
	}
}

// httpsOpts is the HTTPS-enabled options block the HTTPS tests share.
func httpsOpts() Options {
	return Options{
		IPv4Target:   "192.168.1.24",
		HTTPSEnabled: true,
		HTTPS: HTTPSSpec{
			Priority:     5,
			TargetName:   ".",
			Params:       []svcparam.Param{{Key: "alpn", Value: "h2,h3"}},
			AutoIPv4Hint: true,
			AutoIPv6Hint: true,
		},
	}
}

func TestReconcile_HTTPSCreatedWithNewHost(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"new.example.com"}}
	store := newFakeStore()
	r := New(hosts, store, httpsOpts())

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Created) != 1 || res.Created[0] != "new.example.com" {
		t.Fatalf("expected new.example.com to be created, got %+v", res)
	}
	calls := store.calls
	if len(calls) != 1 || calls[0].op != "create" {
		t.Fatalf("expected a single bundled create call, got %+v", calls)
	}
	if len(calls[0].https) != 1 || calls[0].https[0].Priority != 5 {
		t.Fatalf("expected the create call to carry the HTTPS spec, got %+v", calls[0])
	}
	if len(store.hosts["new.example.com"].HTTPSSpecs) != 1 {
		t.Fatalf("expected HTTPS record in the fake zone, got %+v", store.hosts["new.example.com"])
	}
}

func TestReconcile_HTTPSCreatedForExistingHost(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{AIPs: []string{"192.168.1.24"}, OwnedByUs: true}
	r := New(hosts, store, httpsOpts())

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasCall(store.calls, "createHTTPS", "a.example.com") {
		t.Fatalf("expected HTTPS to be created for a.example.com, got %+v", store.calls)
	}
	if len(res.Updated) != 1 || res.Updated[0] != "a.example.com" {
		t.Fatalf("expected host reported as updated, got %+v", res.Updated)
	}
}

func TestReconcile_HTTPSUpdatedWhenSpecDiffers(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{
		AIPs:       []string{"192.168.1.24"},
		HTTPSSpecs: []HTTPSSpec{{Priority: 0, TargetName: ".", Params: []svcparam.Param{{Key: "alpn", Value: "h2,h3"}}, AutoIPv4Hint: true, AutoIPv6Hint: true}},
		OwnedByUs:  true,
	}
	r := New(hosts, store, httpsOpts())

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasCall(store.calls, "updateHTTPS", "a.example.com") {
		t.Fatalf("expected HTTPS to be updated (priority 0 -> 5), got %+v", store.calls)
	}
	if len(res.Updated) != 1 || res.Updated[0] != "a.example.com" {
		t.Fatalf("expected host reported as updated, got %+v", res.Updated)
	}
	if store.hosts["a.example.com"].HTTPSSpecs[0].Priority != 5 {
		t.Fatalf("expected in-place update to priority 5, got %+v", store.hosts["a.example.com"].HTTPSSpecs[0])
	}
}

func TestReconcile_HTTPSUntouchedWhenMatching(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{
		AIPs:       []string{"192.168.1.24"},
		HTTPSSpecs: []HTTPSSpec{httpsOpts().HTTPS},
		OwnedByUs:  true,
	}
	r := New(hosts, store, httpsOpts())

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasCall(store.calls, "updateHTTPS", "a.example.com") || hasCall(store.calls, "createHTTPS", "a.example.com") {
		t.Fatalf("expected no HTTPS mutation when the record already matches, got %+v", store.calls)
	}
	if len(res.Updated) != 0 {
		t.Fatalf("expected no updates, got %+v", res.Updated)
	}
}

func TestReconcile_HTTPSDisabledRemovesExisting(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{
		AIPs:       []string{"192.168.1.24"},
		HTTPSSpecs: []HTTPSSpec{{Priority: 5, TargetName: "."}},
		OwnedByUs:  true,
	}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24"})

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasCall(store.calls, "deleteHTTPSOnly", "a.example.com") {
		t.Fatalf("expected HTTPS to be removed for a.example.com, got %+v", store.calls)
	}
	if len(res.Updated) != 1 {
		t.Fatalf("expected host reported as updated, got %+v", res.Updated)
	}
	if len(store.hosts["a.example.com"].AIPs) == 0 {
		t.Fatalf("A record must survive an HTTPS-only removal")
	}
}

func TestReconcile_SkipsForeignHTTPSOnlyRecord(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"foreign.example.com"}}
	store := newFakeStore()
	// No A record at all, only a foreign HTTPS record with no marker. This
	// must not be treated as "nothing exists yet, safe to create" - it has to
	// be skipped exactly like a foreign A record would be.
	store.hosts["foreign.example.com"] = HostRecords{
		HTTPSSpecs: []HTTPSSpec{{Priority: 1, TargetName: "."}},
		OwnedByUs:  false,
	}
	r := New(hosts, store, httpsOpts())

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "foreign.example.com" {
		t.Fatalf("expected foreign.example.com to be skipped, got %+v", res)
	}
	if hasCall(store.calls, "create", "foreign.example.com") {
		t.Fatalf("must never adopt a foreign HTTPS-only record by creating an A record and marker next to it: %+v", store.calls)
	}
	if len(store.hosts["foreign.example.com"].AIPs) != 0 {
		t.Fatalf("must not have added an A record to a foreign host: %+v", store.hosts["foreign.example.com"])
	}
	if store.hosts["foreign.example.com"].HTTPSSpecs[0].Priority != 1 {
		t.Fatalf("foreign HTTPS record was mutated: %+v", store.hosts["foreign.example.com"].HTTPSSpecs[0])
	}
}

func TestReconcile_DeletesHTTPSOnHostRemoval(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"other.example.com"}}
	store := newFakeStore()
	store.hosts["gone.example.com"] = HostRecords{
		AIPs:       []string{"192.168.1.24"},
		HTTPSSpecs: []HTTPSSpec{{Priority: 5, TargetName: "."}},
		OwnedByUs:  true,
	}
	r := New(hosts, store, httpsOpts())
	r.lastGoodHostCount = 1 // breaker baseline; the removal drops to 1 too

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "gone.example.com" {
		t.Fatalf("expected gone.example.com to be deleted, got %+v", res)
	}
	if !hasCall(store.calls, "delete", "gone.example.com") {
		t.Fatalf("expected DeleteHost call, got %+v", store.calls)
	}
	for _, c := range store.calls {
		if c.op == "delete" && c.hostname == "gone.example.com" && len(c.https) != 1 {
			t.Fatalf("expected the delete call to carry the HTTPS spec for removal, got %+v", c)
		}
	}
	if hasCall(store.calls, "deleteHTTPSOnly", "gone.example.com") {
		t.Fatalf("expected the HTTPS removal to be bundled into DeleteHost, not a separate call: %+v", store.calls)
	}
}

// TestReconcile_AutoHintParamNotComparedWhenAutoOn guards against a perpetual
// update loop: with Automatic Hints on, Technitium overwrites the manual
// ipv4hint value with whatever it resolves from the zone, so the stored
// value must not be compared against the configured one - only the flag is
// compared (the manual value is still sent, per the "send both" design).
func TestReconcile_AutoHintParamNotComparedWhenAutoOn(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{
		AIPs: []string{"192.168.1.24"},
		HTTPSSpecs: []HTTPSSpec{
			{
				Priority:     5,
				TargetName:   ".",
				Params:       []svcparam.Param{{Key: "ipv4hint", Value: "10.9.9.9"}, {Key: "alpn", Value: "h2,h3"}},
				AutoIPv4Hint: true,
				AutoIPv6Hint: true,
			},
		},
		OwnedByUs: true,
	}
	// Desired carries a different manual ipv4hint value, but auto is on for
	// both sides, so the value difference must not trigger an update.
	opts := httpsOpts()
	opts.HTTPS.Params = append(opts.HTTPS.Params, svcparam.Param{Key: "ipv4hint", Value: "1.2.3.4"})
	r := New(hosts, store, opts)

	res, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasCall(store.calls, "updateHTTPS", "a.example.com") {
		t.Fatalf("expected no update when only the auto-managed hint value differs: %+v", store.calls)
	}
	if len(res.Updated) != 0 {
		t.Fatalf("expected no updates, got %+v", res.Updated)
	}
}

// TestReconcile_AutoHintFlagMismatchTriggersUpdate verifies the flip side:
// when the automatic-hint flags differ the record must be updated even if
// everything else matches.
func TestReconcile_AutoHintFlagMismatchTriggersUpdate(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{
		AIPs:       []string{"192.168.1.24"},
		HTTPSSpecs: []HTTPSSpec{httpsOpts().HTTPS},
		OwnedByUs:  true,
	}
	opts := httpsOpts()
	opts.HTTPS.AutoIPv4Hint = false // flip just the flag
	r := New(hosts, store, opts)

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasCall(store.calls, "updateHTTPS", "a.example.com") {
		t.Fatalf("expected an update when the automatic-hint flag differs, got %+v", store.calls)
	}
}

// TestReconcile_HTTPSSpellingAndOrderInsensitive checks that a stored record
// whose params differ only in key spelling or list order compares equal to
// the desired spec (no spurious rewrites), while a real value difference does
// not.
func TestReconcile_HTTPSSpellingAndOrderInsensitive(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{
		AIPs: []string{"192.168.1.24"},
		HTTPSSpecs: []HTTPSSpec{
			{
				Priority:     5,
				TargetName:   ".",
				Params:       []svcparam.Param{{Key: "ALPN", Value: "h2,h3"}, {Key: "no_default_alpn", Value: ""}},
				AutoIPv4Hint: true,
				AutoIPv6Hint: true,
			},
		},
		OwnedByUs: true,
	}
	// Desired spells the keys differently but means the same record.
	opts := httpsOpts()
	opts.HTTPS.Params = []svcparam.Param{
		{Key: "no-default-alpn", Value: ""},
		{Key: "alpn", Value: "h2,h3"},
	}
	r := New(hosts, store, opts)

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasCall(store.calls, "updateHTTPS", "a.example.com") {
		t.Fatalf("expected no update when only key spelling differs: %+v", store.calls)
	}

	// A genuine value difference must still be caught.
	hosts2 := &fakeHostLister{hosts: []string{"a.example.com"}}
	store2 := newFakeStore()
	store2.hosts["a.example.com"] = HostRecords{
		AIPs: []string{"192.168.1.24"},
		HTTPSSpecs: []HTTPSSpec{
			{
				Priority:     5,
				TargetName:   ".",
				Params:       []svcparam.Param{{Key: "alpn", Value: "h2,spooks/1.1"}}, // different ALPN set
				AutoIPv4Hint: true,
				AutoIPv6Hint: true,
			},
		},
		OwnedByUs: true,
	}
	r2 := New(hosts2, store2, httpsOpts())
	if _, err := r2.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasCall(store2.calls, "updateHTTPS", "a.example.com") {
		t.Fatalf("expected an update when the param values genuinely differ, got %+v", store2.calls)
	}
}

func callOps(calls []call) []string {
	ops := make([]string, len(calls))
	for i, c := range calls {
		ops[i] = c.op
	}
	return ops
}

// TestReconcile_HTTPSUpdateRunsBeforeAMutation pins the ordering that keeps
// Technitium's record lookup valid: A/AAAA mutations carry updateSvcbHints,
// which rewrites the HTTPS record's hint params, so the HTTPS update (which
// identifies the record by its current params) must go first.
func TestReconcile_HTTPSUpdateRunsBeforeAMutation(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{
		AIPs:       []string{"10.0.0.9"},
		HTTPSSpecs: []HTTPSSpec{{Priority: 0, TargetName: ".", AutoIPv4Hint: true, AutoIPv6Hint: true}},
		OwnedByUs:  true,
	}
	r := New(hosts, store, httpsOpts())

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ops := callOps(store.calls)
	if len(ops) != 2 || ops[0] != "updateHTTPS" || ops[1] != "updateA" {
		t.Fatalf("expected updateHTTPS before updateA, got %v", ops)
	}
}

// TestReconcile_HTTPSRemovalRunsBeforeAAAARemoval is the same ordering rule
// for the toggle-off path.
func TestReconcile_HTTPSRemovalRunsBeforeAAAARemoval(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{
		AIPs:       []string{"192.168.1.24"},
		AAAAIPs:    []string{"2001:db8::1"},
		HTTPSSpecs: []HTTPSSpec{{Priority: 5, TargetName: ".", AutoIPv4Hint: true, AutoIPv6Hint: true}},
		OwnedByUs:  true,
	}
	r := New(hosts, store, Options{IPv4Target: "192.168.1.24"})

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ops := callOps(store.calls)
	if len(ops) != 2 || ops[0] != "deleteHTTPSOnly" || ops[1] != "deleteAAAAOnly" {
		t.Fatalf("expected deleteHTTPSOnly before deleteAAAAOnly, got %v", ops)
	}
}

// TestReconcile_HTTPSCreateRunsAfterAMutation: a new HTTPS record is created
// last so Technitium resolves its automatic hints from the current A record.
func TestReconcile_HTTPSCreateRunsAfterAMutation(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{AIPs: []string{"10.0.0.9"}, OwnedByUs: true}
	r := New(hosts, store, httpsOpts())

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ops := callOps(store.calls)
	if len(ops) != 2 || ops[0] != "updateA" || ops[1] != "createHTTPS" {
		t.Fatalf("expected updateA before createHTTPS, got %v", ops)
	}
}

// TestReconcile_CreateHostSkipsHTTPSWhenOneExists covers the owned-marker,
// missing-A edge case: an existing HTTPS record must not get a second,
// differing one added next to it.
func TestReconcile_CreateHostSkipsHTTPSWhenOneExists(t *testing.T) {
	hosts := &fakeHostLister{hosts: []string{"a.example.com"}}
	store := newFakeStore()
	store.hosts["a.example.com"] = HostRecords{
		HTTPSSpecs: []HTTPSSpec{{Priority: 9, TargetName: "."}},
		OwnedByUs:  true,
	}
	r := New(hosts, store, httpsOpts())

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range store.calls {
		if c.op == "create" && len(c.https) != 0 {
			t.Fatalf("expected CreateHost without an HTTPS spec, got %+v", c)
		}
	}
}
