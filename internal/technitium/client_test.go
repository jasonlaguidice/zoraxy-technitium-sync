package technitium

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/reconciler"
	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/svcparam"
)

// fakeRecord mirrors one row of Technitium's zone as this client understands
// it: a name, a type, and either an IP (A/AAAA), text (TXT) or SVCB data
// (HTTPS) value.
type fakeRecord struct {
	Name string
	Type string
	IP   string
	Text string

	// HTTPS-only fields.
	Priority     int
	Target       string
	Params       string // pipe-encoded svcParams exactly as the client sent them
	AutoIPv4Hint bool
	AutoIPv6Hint bool
}

// fakeTechnitium is a minimal in-memory stand-in for Technitium's HTTP API,
// enough to exercise this client's request shapes and the envelope handling
// without needing a real server.
type fakeTechnitium struct {
	mu      sync.Mutex
	token   string
	records []fakeRecord
	// lastQueries stores, per API path, the query of the most recent request
	// (used to assert parameters like updateSvcbHints).
	lastQueries map[string]url.Values
}

func newFakeTechnitium(token string) *fakeTechnitium {
	return &fakeTechnitium{token: token, lastQueries: map[string]url.Values{}}
}

func (f *fakeTechnitium) writeEnvelope(w http.ResponseWriter, status string, response any, errMsg string) {
	env := map[string]any{"status": status}
	if response != nil {
		env["response"] = response
	}
	if errMsg != "" {
		env["errorMessage"] = errMsg
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(env)
}

// httpsKey is the fake server's identity of one HTTPS record: its priority,
// target name and params together (Technitium identifies SVCB-family records
// the same way for update/delete).
func httpsKey(q url.Values) (int, string, string) {
	priority, _ := atoi(q.Get("svcPriority"))
	target := strings.Trim(q.Get("svcTargetName"), ".")
	return priority, target, q.Get("svcParams")
}

func atoi(s string) (int, bool) {
	var n int
	ok := false
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
		ok = true
	}
	if !ok {
		return 0, false
	}
	return n, true
}

// parsePipeParams decodes a pipe-encoded svcParams string (or "false") into
// the key/value map Technitium would report for it.
func parsePipeParams(s string) map[string]string {
	out := map[string]string{}
	if s == "" || strings.EqualFold(s, "false") {
		return out
	}
	parts := strings.Split(s, "|")
	for i := 0; i+1 < len(parts); i += 2 {
		out[parts[i]] = parts[i+1]
	}
	return out
}

func (f *fakeTechnitium) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("token") != f.token {
			f.writeEnvelope(w, "invalid-token", nil, "invalid token")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastQueries[r.URL.Path] = q

		switch r.URL.Path {
		case "/api/zones/records/add":
			domain := q.Get("domain")
			typ := strings.ToUpper(q.Get("type"))
			value := q.Get("ipAddress")
			if typ == "TXT" {
				value = q.Get("text")
			}
			if typ == "HTTPS" {
				priority, target, params := httpsKey(q)
				for _, rec := range f.records {
					if rec.Name == domain && rec.Type == typ && rec.Priority == priority && rec.Target == target && rec.Params == params {
						f.writeEnvelope(w, "error", nil, "record already exists in the zone")
						return
					}
				}
				rec := fakeRecord{
					Name: domain, Type: typ,
					Priority: priority, Target: target, Params: params,
					AutoIPv4Hint: q.Get("autoIpv4Hint") == "true",
					AutoIPv6Hint: q.Get("autoIpv6Hint") == "true",
				}
				f.records = append(f.records, rec)
				f.writeEnvelope(w, "ok", map[string]any{}, "")
				return
			}
			for _, rec := range f.records {
				if rec.Name == domain && rec.Type == typ && (rec.IP == value || rec.Text == value) {
					f.writeEnvelope(w, "error", nil, "record already exists in the zone")
					return
				}
			}
			rec := fakeRecord{Name: domain, Type: typ}
			if typ == "TXT" {
				rec.Text = value
			} else {
				rec.IP = value
			}
			f.records = append(f.records, rec)
			f.writeEnvelope(w, "ok", map[string]any{}, "")

		case "/api/zones/records/update":
			domain := q.Get("domain")
			typ := strings.ToUpper(q.Get("type"))
			if typ == "HTTPS" {
				priority, target, params := httpsKey(q)
				// Missing new* params fall back to the current values, the
				// way Technitium's own defaults do.
				newPriority := priority
				if np, ok := atoi(q.Get("newSvcPriority")); ok {
					newPriority = np
				}
				newTarget := target
				if q.Get("newSvcTargetName") != "" {
					newTarget = strings.Trim(q.Get("newSvcTargetName"), ".")
				}
				newParams := params
				if q.Get("newSvcParams") != "" {
					newParams = q.Get("newSvcParams")
				}
				for idx, rec := range f.records {
					if rec.Name == domain && rec.Type == typ && rec.Priority == priority && rec.Target == target && rec.Params == params {
						f.records[idx].Priority = newPriority
						f.records[idx].Target = newTarget
						f.records[idx].Params = newParams
						f.records[idx].AutoIPv4Hint = q.Get("autoIpv4Hint") == "true"
						f.records[idx].AutoIPv6Hint = q.Get("autoIpv6Hint") == "true"
						f.writeEnvelope(w, "ok", map[string]any{}, "")
						return
					}
				}
				f.writeEnvelope(w, "error", nil, "record does not exist")
				return
			}
			oldVal := q.Get("ipAddress")
			newVal := q.Get("newIpAddress")
			for i, rec := range f.records {
				if rec.Name == domain && rec.Type == typ && rec.IP == oldVal {
					f.records[i].IP = newVal
					f.writeEnvelope(w, "ok", map[string]any{}, "")
					return
				}
			}
			f.writeEnvelope(w, "error", nil, "record does not exist")

		case "/api/zones/records/delete":
			domain := q.Get("domain")
			typ := strings.ToUpper(q.Get("type"))
			value := q.Get("ipAddress")
			if typ == "TXT" {
				value = q.Get("text")
			}
			if typ == "HTTPS" {
				priority, target, params := httpsKey(q)
				for i, rec := range f.records {
					if rec.Name == domain && rec.Type == typ && rec.Priority == priority && rec.Target == target && rec.Params == params {
						f.records = append(f.records[:i], f.records[i+1:]...)
						f.writeEnvelope(w, "ok", map[string]any{}, "")
						return
					}
				}
				f.writeEnvelope(w, "error", nil, "record does not exist")
				return
			}
			for i, rec := range f.records {
				if rec.Name == domain && rec.Type == typ && (rec.IP == value || rec.Text == value) {
					f.records = append(f.records[:i], f.records[i+1:]...)
					f.writeEnvelope(w, "ok", map[string]any{}, "")
					return
				}
			}
			f.writeEnvelope(w, "error", nil, "record does not exist")

		case "/api/zones/records/get":
			listZone := q.Get("listZone") == "true"
			domain := q.Get("domain")
			var out []map[string]any
			for _, rec := range f.records {
				if !listZone && rec.Name != domain {
					continue
				}
				row := map[string]any{"name": rec.Name, "type": rec.Type}
				switch rec.Type {
				case "TXT":
					row["rData"] = map[string]any{"text": rec.Text}
				case "HTTPS":
					row["rData"] = map[string]any{
						"svcPriority":   rec.Priority,
						"svcTargetName": rec.Target,
						"svcParams":     parsePipeParams(rec.Params),
						"autoIpv4Hint":  rec.AutoIPv4Hint,
						"autoIpv6Hint":  rec.AutoIPv6Hint,
					}
				default:
					row["rData"] = map[string]any{"ipAddress": rec.IP}
				}
				out = append(out, row)
			}
			f.writeEnvelope(w, "ok", map[string]any{"records": out}, "")

		default:
			http.NotFound(w, r)
		}
	}
}

func newTestClient(t *testing.T, fake *fakeTechnitium) *Client {
	t.Helper()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	return New(srv.URL, fake.token, "example.com", 300, "instance-1")
}

func TestAddA_Success(t *testing.T) {
	fake := newFakeTechnitium("tok")
	c := newTestClient(t, fake)

	if err := c.addA(context.Background(), "app.example.com", "1.2.3.4"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.records) != 1 || fake.records[0].IP != "1.2.3.4" {
		t.Fatalf("record not added: %+v", fake.records)
	}
}

// TestAMutationsCarryUpdateSvcbHints verifies the A/AAAA add/update/delete
// requests ask Technitium to refresh Automatic Hints in the zone's HTTPS
// records - that is what keeps automatic ipv4hint/ipv6hint params in sync
// when the LAN target changes.
func TestAMutationsCarryUpdateSvcbHints(t *testing.T) {
	fake := newFakeTechnitium("tok")
	c := newTestClient(t, fake)

	if err := c.addA(context.Background(), "app.example.com", "1.2.3.4"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := c.updateA(context.Background(), "app.example.com", "1.2.3.4", "2.2.2.2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := c.deleteA(context.Background(), "app.example.com", "2.2.2.2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := c.addAAAA(context.Background(), "app.example.com", "fd00::1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := c.updateAAAA(context.Background(), "app.example.com", "fd00::1", "fd00::2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := c.deleteAAAA(context.Background(), "app.example.com", "fd00::2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, path := range []string{
		"/api/zones/records/add",
		"/api/zones/records/update",
		"/api/zones/records/delete",
	} {
		q := fake.lastQueries[path]
		if q == nil {
			t.Fatalf("expected a recorded query for %s", path)
		}
		if q.Get("updateSvcbHints") != "true" {
			t.Fatalf("expected updateSvcbHints=true on %s, got %+v", path, q)
		}
	}

	// TXT mutations are not A/AAAA records; the marker add must not claim
	// hint updates.
	if err := c.addTXT(context.Background(), "_ztsync.app.example.com", "heritage=x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q := fake.lastQueries["/api/zones/records/add"]; q.Get("updateSvcbHints") != "" {
		t.Fatalf("expected no updateSvcbHints on a TXT add, got %+v", q)
	}
}

func TestAddA_AlreadyExistsIsBenign(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records, fakeRecord{Name: "app.example.com", Type: "A", IP: "1.2.3.4"})
	c := newTestClient(t, fake)

	if err := c.addA(context.Background(), "app.example.com", "1.2.3.4"); err != nil {
		t.Fatalf("expected benign conflict to be treated as success, got: %v", err)
	}
}

func TestAddA_RealErrorIsSurfaced(t *testing.T) {
	fake := newFakeTechnitium("tok")
	c := newTestClient(t, fake)
	// Wrong token triggers a real (non-benign) error.
	c.Token = "wrong"

	err := c.addA(context.Background(), "app.example.com", "1.2.3.4")
	if err == nil {
		t.Fatalf("expected an error for invalid token")
	}
	if strings.Contains(strings.ToLower(err.Error()), "already exists") {
		t.Fatalf("invalid-token error must not be treated as benign: %v", err)
	}
}

func TestUpdateA_RequiresOwnershipMarker(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records, fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"})
	c := newTestClient(t, fake)

	err := c.UpdateA(context.Background(), "app.example.com", "1.1.1.1", "2.2.2.2")
	if err != ErrNotOwned {
		t.Fatalf("expected ErrNotOwned when marker is missing, got: %v", err)
	}
	if fake.records[0].IP != "1.1.1.1" {
		t.Fatalf("record must not be mutated when unowned: %+v", fake.records[0])
	}
}

func TestUpdateA_ProceedsWhenMarkerMatches(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records,
		fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=instance-1"},
	)
	c := newTestClient(t, fake)

	if err := c.UpdateA(context.Background(), "app.example.com", "1.1.1.1", "2.2.2.2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.records[0].IP != "2.2.2.2" {
		t.Fatalf("expected record to be updated, got: %+v", fake.records[0])
	}
}

func TestUpdateA_RefusesWhenMarkerBelongsToDifferentInstance(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records,
		fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=someone-else"},
	)
	c := newTestClient(t, fake)

	err := c.UpdateA(context.Background(), "app.example.com", "1.1.1.1", "2.2.2.2")
	if err != ErrNotOwned {
		t.Fatalf("expected ErrNotOwned for a marker from a different instance, got: %v", err)
	}
	if fake.records[0].IP != "1.1.1.1" {
		t.Fatalf("record must not be mutated: %+v", fake.records[0])
	}
}

func TestDeleteHost_RequiresOwnershipMarker(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records, fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"})
	c := newTestClient(t, fake)

	err := c.DeleteHost(context.Background(), "app.example.com", "1.1.1.1", "", nil)
	if err != ErrNotOwned {
		t.Fatalf("expected ErrNotOwned when marker is missing, got: %v", err)
	}
	if len(fake.records) != 1 {
		t.Fatalf("record must survive an unowned delete attempt: %+v", fake.records)
	}
}

func TestDeleteHost_DeletesAllRecordsWhenOwned(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records,
		fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"},
		fakeRecord{Name: "app.example.com", Type: "AAAA", IP: "fd00::1"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=instance-1"},
	)
	c := newTestClient(t, fake)

	if err := c.DeleteHost(context.Background(), "app.example.com", "1.1.1.1", "fd00::1", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.records) != 0 {
		t.Fatalf("expected all records removed, got: %+v", fake.records)
	}
}

func TestGetZoneState_GroupsRecordsAndOwnership(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records,
		fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=instance-1"},
		fakeRecord{Name: "manual.example.com", Type: "A", IP: "9.9.9.9"},
		fakeRecord{Name: "orphaned.example.com", Type: "A", IP: "8.8.8.8"},
		fakeRecord{Name: "_ztsync.orphaned.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=other-instance"},
	)
	c := newTestClient(t, fake)

	state, err := c.GetZoneState(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !state.Hosts["app.example.com"].OwnedByUs {
		t.Fatalf("expected app.example.com to be owned by us: %+v", state.Hosts["app.example.com"])
	}
	if state.Hosts["manual.example.com"].OwnedByUs {
		t.Fatalf("manual.example.com has no marker and must not be owned")
	}
	if state.Hosts["orphaned.example.com"].OwnedByUs {
		t.Fatalf("orphaned.example.com's marker belongs to another instance and must not be owned")
	}
	if len(state.Hosts["app.example.com"].AIPs) != 1 || state.Hosts["app.example.com"].AIPs[0] != "1.1.1.1" {
		t.Fatalf("unexpected A records for app.example.com: %+v", state.Hosts["app.example.com"])
	}
}

// TestGetZoneState_ForeignLeftoverTXTDoesNotMaskRealMarker covers a stale or
// foreign TXT value sitting at the same _ztsync.<hostname> name as our real
// marker (e.g. left over from an instance ID change). GetZoneState must
// still report ownership as true because our marker IS present among the
// records at that name, matching what checkOwnership (a full scan) would
// find at mutation time - a naive "last record wins" reducer could instead
// report false depending on response order, which is exactly the disagreement
// this test guards against.
func TestGetZoneState_ForeignLeftoverTXTDoesNotMaskRealMarker(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records,
		fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"},
		// Foreign/stale TXT listed BEFORE our real marker...
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=old-instance"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=instance-1"},
	)
	c := newTestClient(t, fake)

	state, err := c.GetZoneState(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !state.Hosts["app.example.com"].OwnedByUs {
		t.Fatalf("expected app.example.com to be owned by us despite a foreign TXT at the same name: %+v", state.Hosts["app.example.com"])
	}

	// And the reverse order must give the same answer - ownership must not
	// depend on which record the server happened to list last.
	fake2 := newFakeTechnitium("tok")
	fake2.records = append(fake2.records,
		fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=instance-1"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=old-instance"},
	)
	c2 := newTestClient(t, fake2)
	state2, err := c2.GetZoneState(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !state2.Hosts["app.example.com"].OwnedByUs {
		t.Fatalf("expected app.example.com to be owned by us regardless of record order: %+v", state2.Hosts["app.example.com"])
	}
}

func TestGetZoneState_SurfacesTransportError(t *testing.T) {
	c := New("http://127.0.0.1:0", "tok", "example.com", 300, "instance-1")
	if _, err := c.GetZoneState(context.Background()); err == nil {
		t.Fatalf("expected an error when the server is unreachable")
	}
}

func TestCreateHost_AddsMarkerAndRecords(t *testing.T) {
	fake := newFakeTechnitium("tok")
	c := newTestClient(t, fake)

	if err := c.CreateHost(context.Background(), "new.example.com", "1.2.3.4", "fd00::5", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sawA, sawAAAA, sawTXT bool
	for _, rec := range fake.records {
		switch {
		case rec.Name == "new.example.com" && rec.Type == "A" && rec.IP == "1.2.3.4":
			sawA = true
		case rec.Name == "new.example.com" && rec.Type == "AAAA" && rec.IP == "fd00::5":
			sawAAAA = true
		case rec.Name == "_ztsync.new.example.com" && rec.Type == "TXT" && strings.Contains(rec.Text, "instance-1"):
			sawTXT = true
		}
	}
	if !sawA || !sawAAAA || !sawTXT {
		t.Fatalf("expected A, AAAA and TXT marker to be created, got: %+v", fake.records)
	}
}

func TestCreateHost_WithoutIPv6SkipsAAAA(t *testing.T) {
	fake := newFakeTechnitium("tok")
	c := newTestClient(t, fake)

	if err := c.CreateHost(context.Background(), "new.example.com", "1.2.3.4", "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, rec := range fake.records {
		if rec.Type == "AAAA" {
			t.Fatalf("did not expect an AAAA record: %+v", fake.records)
		}
	}
}

func TestCreateHost_WithoutHTTPSSpecSkipsHTTPS(t *testing.T) {
	fake := newFakeTechnitium("tok")
	c := newTestClient(t, fake)

	if err := c.CreateHost(context.Background(), "new.example.com", "1.2.3.4", "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, rec := range fake.records {
		if rec.Type == "HTTPS" {
			t.Fatalf("did not expect an HTTPS record: %+v", fake.records)
		}
	}
}

func TestCreateHost_WithHTTPSSpecCreatesIt(t *testing.T) {
	fake := newFakeTechnitium("tok")
	c := newTestClient(t, fake)

	spec := reconciler.HTTPSSpec{
		Priority:     5,
		TargetName:   "",
		Params:       []svcparam.Param{{Key: "alpn", Value: "h2,h3"}, {Key: "port", Value: "443"}},
		AutoIPv4Hint: true,
		AutoIPv6Hint: false,
	}
	if err := c.CreateHost(context.Background(), "new.example.com", "1.2.3.4", "", []reconciler.HTTPSSpec{spec}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sawHTTPS bool
	for _, rec := range fake.records {
		if rec.Name == "new.example.com" && rec.Type == "HTTPS" {
			sawHTTPS = true
			if rec.Priority != 5 || rec.Target != "" || rec.Params != "alpn|h2,h3|port|443" {
				t.Fatalf("unexpected HTTPS record: %+v", rec)
			}
			if !rec.AutoIPv4Hint || rec.AutoIPv6Hint {
				t.Fatalf("unexpected auto flags: %+v", rec)
			}
		}
	}
	if !sawHTTPS {
		t.Fatalf("expected an HTTPS record, got: %+v", fake.records)
	}
}

// TestCreateHost_HTTPSOnlyWhenOwned ensures the HTTPS creation only happens
// after the marker and A record succeeded (so a failed A creation cannot
// leave an HTTPS record without its ownership marker).
func TestCreateHost_HTTPSOnlyWhenOwned(t *testing.T) {
	fake := newFakeTechnitium("tok")
	c := newTestClient(t, fake)
	// Make the A add fail by using an invalid token.
	c.Token = "wrong"

	spec := reconciler.HTTPSSpec{Priority: 1, TargetName: "."}
	err := c.CreateHost(context.Background(), "new.example.com", "1.2.3.4", "", []reconciler.HTTPSSpec{spec})
	if err == nil {
		t.Fatalf("expected the A creation failure to surface")
	}
	for _, rec := range fake.records {
		if rec.Type == "HTTPS" {
			t.Fatalf("HTTPS record must not exist when the A creation failed: %+v", fake.records)
		}
	}
}

func TestCreateHTTPS_RequiresOwnershipMarker(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records, fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"})
	c := newTestClient(t, fake)

	err := c.CreateHTTPS(context.Background(), "app.example.com", reconciler.HTTPSSpec{Priority: 1, TargetName: "."})
	if err != ErrNotOwned {
		t.Fatalf("expected ErrNotOwned when marker is missing, got: %v", err)
	}
	for _, rec := range fake.records {
		if rec.Type == "HTTPS" {
			t.Fatalf("record must not be created when unowned: %+v", fake.records)
		}
	}
}

func TestCreateHTTPS_SendsSpec(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records,
		fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=instance-1"},
	)
	c := newTestClient(t, fake)

	spec := reconciler.HTTPSSpec{
		Priority:     3,
		TargetName:   ".",
		Params:       []svcparam.Param{{Key: "alpn", Value: "h2,h3"}, {Key: "no-default-alpn", Value: ""}},
		AutoIPv4Hint: true,
		AutoIPv6Hint: true,
	}
	if err := c.CreateHTTPS(context.Background(), "app.example.com", spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, rec := range fake.records {
		if rec.Type == "HTTPS" {
			if rec.Priority != 3 || rec.Target != "" || rec.Params != "alpn|h2,h3|no-default-alpn|" {
				t.Fatalf("unexpected HTTPS record: %+v", rec)
			}
			if !rec.AutoIPv4Hint || !rec.AutoIPv6Hint {
				t.Fatalf("unexpected auto flags: %+v", rec)
			}
		}
	}
}

func TestUpdateHTTPS_RequiresOwnershipMarker(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records, fakeRecord{Name: "app.example.com", Type: "HTTPS", Priority: 1, Target: "", Params: "false"})
	c := newTestClient(t, fake)

	current := reconciler.HTTPSSpec{Priority: 1, TargetName: "."}
	desired := reconciler.HTTPSSpec{Priority: 5, TargetName: "."}
	err := c.UpdateHTTPS(context.Background(), "app.example.com", current, desired)
	if err != ErrNotOwned {
		t.Fatalf("expected ErrNotOwned when marker is missing, got: %v", err)
	}
	if fake.records[0].Priority != 1 {
		t.Fatalf("record must not be mutated when unowned: %+v", fake.records[0])
	}
}

func TestUpdateHTTPS_ProceedsWhenOwned(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records,
		fakeRecord{Name: "app.example.com", Type: "HTTPS", Priority: 1, Target: "", Params: "false"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=instance-1"},
	)
	c := newTestClient(t, fake)

	current := reconciler.HTTPSSpec{Priority: 1, TargetName: "."}
	desired := reconciler.HTTPSSpec{
		Priority:     5,
		TargetName:   ".",
		Params:       []svcparam.Param{{Key: "alpn", Value: "h2,h3"}},
		AutoIPv4Hint: true,
		AutoIPv6Hint: true,
	}
	if err := c.UpdateHTTPS(context.Background(), "app.example.com", current, desired); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rec := fake.records[0]
	if rec.Priority != 5 || rec.Target != "" || rec.Params != "alpn|h2,h3" || !rec.AutoIPv4Hint || !rec.AutoIPv6Hint {
		t.Fatalf("expected record to be updated in place, got: %+v", rec)
	}
}

func TestDeleteHTTPSOnly_RequiresOwnershipMarker(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records, fakeRecord{Name: "app.example.com", Type: "HTTPS", Priority: 5, Target: "", Params: "false"})
	c := newTestClient(t, fake)

	err := c.DeleteHTTPSOnly(context.Background(), "app.example.com", reconciler.HTTPSSpec{Priority: 5, TargetName: "."})
	if err != ErrNotOwned {
		t.Fatalf("expected ErrNotOwned when marker is missing, got: %v", err)
	}
	if len(fake.records) != 1 {
		t.Fatalf("record must survive an unowned delete attempt: %+v", fake.records)
	}
}

func TestDeleteHTTPSOnly_DeletesOnlyHTTPS(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records,
		fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"},
		fakeRecord{Name: "app.example.com", Type: "HTTPS", Priority: 5, Target: "", Params: "false"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=instance-1"},
	)
	c := newTestClient(t, fake)

	if err := c.DeleteHTTPSOnly(context.Background(), "app.example.com", reconciler.HTTPSSpec{Priority: 5, TargetName: "."}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, rec := range fake.records {
		if rec.Type == "HTTPS" {
			t.Fatalf("expected the HTTPS record gone, got: %+v", fake.records)
		}
	}
	if len(fake.records) != 2 {
		t.Fatalf("expected A record and marker to survive, got: %+v", fake.records)
	}
}

func TestDeleteHost_DeletesHTTPSWhenOwned(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records,
		fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"},
		fakeRecord{Name: "app.example.com", Type: "HTTPS", Priority: 5, Target: "", Params: "alpn|h2,h3"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=instance-1"},
	)
	c := newTestClient(t, fake)

	https := []reconciler.HTTPSSpec{{Priority: 5, TargetName: ".", Params: []svcparam.Param{{Key: "alpn", Value: "h2,h3"}}}}
	if err := c.DeleteHost(context.Background(), "app.example.com", "1.1.1.1", "", https); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.records) != 0 {
		t.Fatalf("expected all records removed, got: %+v", fake.records)
	}
}

func TestGetZoneState_ParsesHTTPSRecords(t *testing.T) {
	fake := newFakeTechnitium("tok")
	fake.records = append(fake.records,
		fakeRecord{Name: "app.example.com", Type: "A", IP: "1.1.1.1"},
		fakeRecord{Name: "_ztsync.app.example.com", Type: "TXT", Text: "heritage=zoraxy-technitium-sync,instance=instance-1"},
		fakeRecord{
			Name: "app.example.com", Type: "HTTPS",
			Priority: 5, Target: "", Params: "alpn|h2,h3|port|443|65|010203",
			AutoIPv4Hint: true, AutoIPv6Hint: false,
		},
	)
	c := newTestClient(t, fake)

	state, err := c.GetZoneState(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hr := state.Hosts["app.example.com"]
	if len(hr.HTTPSSpecs) != 1 {
		t.Fatalf("expected one HTTPS record parsed, got: %+v", hr)
	}
	spec := hr.HTTPSSpecs[0]
	if spec.Priority != 5 || spec.TargetName != "" || !spec.AutoIPv4Hint || spec.AutoIPv6Hint {
		t.Fatalf("unexpected HTTPS spec: %+v", spec)
	}
	if svcparam.CanonicalParams(spec.Params) != svcparam.CanonicalParams([]svcparam.Param{
		{Key: "alpn", Value: "h2,h3"},
		{Key: "port", Value: "443"},
		{Key: "65", Value: "01:02:03"},
	}) {
		t.Fatalf("unexpected params: %+v", spec.Params)
	}
}
