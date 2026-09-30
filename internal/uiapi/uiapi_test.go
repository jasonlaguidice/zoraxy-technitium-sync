package uiapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/config"
	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/status"
	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/svcparam"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return &Server{Config: config.NewStore(cfg, path), Status: &status.Store{}}
}

func postConfig(s *Server, payload configWritePayload, withCSRF bool) *httptest.ResponseRecorder {
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config", bytes.NewReader(body))
	if withCSRF {
		req.Header.Set("X-Zoraxy-Csrf", "test-csrf-token")
	}
	rec := httptest.NewRecorder()
	s.handleConfig(rec, req)
	return rec
}

func TestHandleConfig_GetReturnsCurrentConfig(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ui/api/config", nil)
	rec := httptest.NewRecorder()

	s.handleConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload configReadPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A fresh config has no built-in Technitium zone default any more: it's
	// specific to the operator's own DNS server, so it starts blank.
	if payload.Zone != "" {
		t.Fatalf("expected a fresh config to have a blank zone, got %q", payload.Zone)
	}
}

func TestHandleConfig_PostUpdatesConfig(t *testing.T) {
	s := newTestServer(t)
	s.Trigger = make(chan struct{}, 1)

	rec := postConfig(s, configWritePayload{
		TechnitiumBaseURL:   "http://10.0.0.1:5380",
		Zone:                "example.org",
		TTLSeconds:          120,
		PollIntervalSeconds: 60,
		LANIPv4:             "10.0.0.2",
	}, true)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if s.Config.Snapshot().Zone != "example.org" {
		t.Fatalf("expected config to be updated, got %+v", s.Config.Snapshot())
	}
	select {
	case <-s.Trigger:
	default:
		t.Fatalf("expected trigger signal after successful save")
	}
}

func TestHandleConfig_PostRejectsInvalidPayload(t *testing.T) {
	s := newTestServer(t)
	rec := postConfig(s, configWritePayload{Zone: ""}, true)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleConfig_PostWithoutCSRFHeaderIsRejected(t *testing.T) {
	s := newTestServer(t)
	before := s.Config.Snapshot()

	rec := postConfig(s, configWritePayload{
		TechnitiumBaseURL:   "http://10.0.0.1:5380",
		Zone:                "should-not-apply.example",
		TTLSeconds:          120,
		PollIntervalSeconds: 60,
		LANIPv4:             "10.0.0.2",
	}, false)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a CSRF header, got %d: %s", rec.Code, rec.Body.String())
	}
	after := s.Config.Snapshot()
	if after.Zone != before.Zone {
		t.Fatalf("config must not change when the CSRF header is missing: before=%q after=%q", before.Zone, after.Zone)
	}
}

func TestHandleConfig_GetNeverReturnsRawToken(t *testing.T) {
	s := newTestServer(t)
	const secret = "super-secret-token-value"
	rec := postConfig(s, configWritePayload{
		TechnitiumBaseURL:   "http://10.0.0.1:5380",
		TechnitiumToken:     secret,
		Zone:                "example.org",
		TTLSeconds:          config.DefaultTTLSeconds,
		PollIntervalSeconds: config.DefaultPollIntervalSeconds,
		LANIPv4:             "10.0.0.2",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup POST failed: %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("POST response must not echo the raw token: %s", rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/api/config", nil)
	getRec := httptest.NewRecorder()
	s.handleConfig(getRec, req)

	if strings.Contains(getRec.Body.String(), secret) {
		t.Fatalf("GET /api/config must never contain the raw token, got: %s", getRec.Body.String())
	}
	var payload configReadPayload
	if err := json.Unmarshal(getRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !payload.TechnitiumTokenSet {
		t.Fatalf("expected technitium_token_set to be true once a token is stored")
	}
}

func TestHandleConfig_PostWithBlankTokenPreservesExisting(t *testing.T) {
	s := newTestServer(t)
	const secret = "super-secret-token-value"

	rec := postConfig(s, configWritePayload{
		TechnitiumBaseURL:   "http://10.0.0.1:5380",
		TechnitiumToken:     secret,
		Zone:                "example.org",
		TTLSeconds:          config.DefaultTTLSeconds,
		PollIntervalSeconds: config.DefaultPollIntervalSeconds,
		LANIPv4:             "10.0.0.2",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup POST failed: %d: %s", rec.Code, rec.Body.String())
	}

	rec = postConfig(s, configWritePayload{
		TechnitiumBaseURL:   "http://10.0.0.1:5380",
		TechnitiumToken:     "", // blank: must not clear the stored token
		Zone:                "changed.example.org",
		TTLSeconds:          config.DefaultTTLSeconds,
		PollIntervalSeconds: config.DefaultPollIntervalSeconds,
		LANIPv4:             "10.0.0.2",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("second POST failed: %d: %s", rec.Code, rec.Body.String())
	}

	snap := s.Config.Snapshot()
	if snap.TechnitiumToken != secret {
		t.Fatalf("expected existing token to be preserved, got %q", snap.TechnitiumToken)
	}
	if snap.Zone != "changed.example.org" {
		t.Fatalf("expected other fields to still be updated, got %+v", snap)
	}
}

func TestHandleStatus_ReturnsSnapshot(t *testing.T) {
	s := newTestServer(t)
	s.Status.Set(status.Snapshot{ManagedCount: 3, LastSuccess: true})

	req := httptest.NewRequest(http.MethodGet, "/ui/api/status", nil)
	rec := httptest.NewRecorder()
	s.handleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var snap status.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.ManagedCount != 3 || !snap.LastSuccess {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

// TestHandleConfig_HTTPSFieldsRoundTrip checks the HTTPS record settings
// survive a POST -> persist -> GET round trip, including explicit off values
// for the automatic hints, and that a fresh config's GET reports the
// default-on hints.
func TestHandleConfig_HTTPSFieldsRoundTrip(t *testing.T) {
	s := newTestServer(t)

	// A fresh config has no stored hint flags; the read payload must report
	// the default-on values.
	req := httptest.NewRequest(http.MethodGet, "/ui/api/config", nil)
	rec := httptest.NewRecorder()
	s.handleConfig(rec, req)
	var fresh configReadPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &fresh); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fresh.HTTPSEnabled || fresh.HTTPSPriority != 1 || len(fresh.HTTPSParams) != 0 {
		t.Fatalf("unexpected fresh payload: %+v", fresh)
	}
	if !fresh.HTTPSAutoIPv4Hint || !fresh.HTTPSAutoIPv6Hint {
		t.Fatalf("expected default-on automatic hints, got %+v", fresh)
	}

	// Seed a valid config first (the fresh one has no Technitium URL/zone).
	rec = postConfig(s, configWritePayload{
		TechnitiumBaseURL:   "http://10.0.0.1:5380",
		Zone:                "example.org",
		TTLSeconds:          120,
		PollIntervalSeconds: 60,
		LANIPv4:             "10.0.0.2",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup POST failed: %d: %s", rec.Code, rec.Body.String())
	}

	// Now save the HTTPS settings, with the hints explicitly off.
	rec = postConfig(s, configWritePayload{
		TechnitiumBaseURL:   "http://10.0.0.1:5380",
		Zone:                "example.org",
		TTLSeconds:          120,
		PollIntervalSeconds: 60,
		LANIPv4:             "10.0.0.2",
		HTTPSEnabled:        true,
		HTTPSPriority:       intPtr(5),
		HTTPSTargetName:     "app.example.com",
		HTTPSParams: []svcparam.Param{
			{Key: "alpn", Value: "h2,h3"},
			{Key: "port", Value: "443"},
			{Key: "65", Value: "010203"},
		},
		HTTPSAutoIPv4Hint: boolPtr(false),
		HTTPSAutoIPv6Hint: boolPtr(false),
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	snap := s.Config.Snapshot()
	if !snap.HTTPSEnabled || snap.HTTPSPriorityValue() != 5 || snap.HTTPSTargetName != "app.example.com" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if len(snap.HTTPSParams) != 3 || snap.HTTPSParams[2].Key != "65" || snap.HTTPSParams[2].Value != "010203" {
		t.Fatalf("unexpected params: %+v", snap.HTTPSParams)
	}
	if snap.AutoIPv4HintEnabled() || snap.AutoIPv6HintEnabled() {
		t.Fatalf("expected explicitly-off hints to persist, got %+v", snap)
	}

	// And the read payload mirrors what was saved.
	req = httptest.NewRequest(http.MethodGet, "/ui/api/config", nil)
	rec = httptest.NewRecorder()
	s.handleConfig(rec, req)
	var payload configReadPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !payload.HTTPSEnabled || payload.HTTPSPriority != 5 || payload.HTTPSTargetName != "app.example.com" {
		t.Fatalf("unexpected read payload: %+v", payload)
	}
	if len(payload.HTTPSParams) != 3 || !payloadHasParam(payload.HTTPSParams, "port", "443") {
		t.Fatalf("unexpected read payload params: %+v", payload.HTTPSParams)
	}
	if payload.HTTPSAutoIPv4Hint || payload.HTTPSAutoIPv6Hint {
		t.Fatalf("unexpected read payload hints: %+v", payload)
	}
}

func payloadHasParam(params []svcparam.Param, key, value string) bool {
	for _, p := range params {
		if p.Key == key && p.Value == value {
			return true
		}
	}
	return false
}

// TestHandleConfig_PostRejectsInvalidHTTPSParam checks that an invalid
// key/value pair is rejected with 400 and does not disturb the stored
// config.
func TestHandleConfig_PostRejectsInvalidHTTPSParam(t *testing.T) {
	s := newTestServer(t)
	rec := postConfig(s, configWritePayload{
		TechnitiumBaseURL:   "http://10.0.0.1:5380",
		Zone:                "example.org",
		TTLSeconds:          120,
		PollIntervalSeconds: 60,
		LANIPv4:             "10.0.0.2",
		HTTPSEnabled:        true,
		HTTPSParams:         []svcparam.Param{{Key: "port", Value: "abc"}},
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if s.Config.Snapshot().HTTPSEnabled {
		t.Fatalf("rejected update must not mutate the live config")
	}
}

func intPtr(n int) *int { return &n }

func boolPtr(b bool) *bool { return &b }

// TestHandleConfig_PostWithoutHTTPSHintFieldsKeepsDefaults checks that a
// client that omits the hint flags and priority (e.g. an older UI) does not
// silently switch the automatic hints off or zero the priority.
func TestHandleConfig_PostWithoutHTTPSHintFieldsKeepsDefaults(t *testing.T) {
	s := newTestServer(t)

	rec := postConfig(s, configWritePayload{
		TechnitiumBaseURL:   "http://10.0.0.1:5380",
		Zone:                "example.org",
		TTLSeconds:          120,
		PollIntervalSeconds: 60,
		LANIPv4:             "10.0.0.2",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	snap := s.Config.Snapshot()
	if !snap.AutoIPv4HintEnabled() || !snap.AutoIPv6HintEnabled() {
		t.Fatalf("expected omitted hint flags to stay on, got %+v", snap)
	}
	if snap.HTTPSPriorityValue() != 1 {
		t.Fatalf("expected omitted priority to default to 1, got %d", snap.HTTPSPriorityValue())
	}
}
