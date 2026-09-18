package technitium

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeRecord mirrors one row of Technitium's zone as this client understands
// it: a name, a type, and either an IP (A/AAAA) or text (TXT) value.
type fakeRecord struct {
	Name string
	Type string
	IP   string
	Text string
}

// fakeTechnitium is a minimal in-memory stand-in for Technitium's HTTP API,
// enough to exercise this client's request shapes and the envelope handling
// without needing a real server.
type fakeTechnitium struct {
	mu      sync.Mutex
	token   string
	records []fakeRecord
}

func newFakeTechnitium(token string) *fakeTechnitium {
	return &fakeTechnitium{token: token}
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

func (f *fakeTechnitium) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("token") != f.token {
			f.writeEnvelope(w, "invalid-token", nil, "invalid token")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()

		switch r.URL.Path {
		case "/api/zones/records/add":
			domain := q.Get("domain")
			typ := strings.ToUpper(q.Get("type"))
			value := q.Get("ipAddress")
			if typ == "TXT" {
				value = q.Get("text")
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
				if rec.Type == "TXT" {
					row["rData"] = map[string]any{"text": rec.Text}
				} else {
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

	err := c.DeleteHost(context.Background(), "app.example.com", "1.1.1.1", "")
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

	if err := c.DeleteHost(context.Background(), "app.example.com", "1.1.1.1", "fd00::1"); err != nil {
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

	if err := c.CreateHost(context.Background(), "new.example.com", "1.2.3.4", "fd00::5"); err != nil {
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

	if err := c.CreateHost(context.Background(), "new.example.com", "1.2.3.4", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, rec := range fake.records {
		if rec.Type == "AAAA" {
			t.Fatalf("did not expect an AAAA record: %+v", fake.records)
		}
	}
}
