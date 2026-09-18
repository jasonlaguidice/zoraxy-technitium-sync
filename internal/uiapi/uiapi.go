// Package uiapi exposes the small JSON API the embedded web UI calls to read
// and edit the plugin's config and to read the last reconcile status. It has
// no knowledge of Zoraxy or Technitium; it only mediates between HTTP and
// the config/status stores.
package uiapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/config"
	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/status"
)

// Server wires the config and status stores to HTTP handlers.
type Server struct {
	Config *config.Store
	Status *status.Store
	// Trigger, if non-nil, receives a non-blocking signal after every
	// successful config save so the reconcile loop can pick up the change
	// immediately instead of waiting for the next scheduled tick.
	Trigger chan struct{}
}

// Register attaches the API handlers under <uiPath>/api/*. Call this before
// attaching the embedded static-file router, which claims <uiPath>/ as a
// catch-all; net/http's ServeMux prefers the more specific registered
// patterns regardless of registration order.
func (s *Server) Register(uiPath string, mux *http.ServeMux) {
	mux.HandleFunc(uiPath+"/api/config", s.handleConfig)
	mux.HandleFunc(uiPath+"/api/status", s.handleStatus)
}

// configReadPayload is what GET /api/config returns. It deliberately omits
// the raw Technitium token - only whether one is set - so the secret is
// never echoed back to the browser (or to anything reading the API).
type configReadPayload struct {
	TechnitiumBaseURL   string `json:"technitium_base_url"`
	TechnitiumTokenSet  bool   `json:"technitium_token_set"`
	Zone                string `json:"zone"`
	TTLSeconds          int    `json:"ttl_seconds"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	LANIPv4             string `json:"lan_ipv4"`
	AAAAEnabled         bool   `json:"aaaa_enabled"`
	LANIPv6             string `json:"lan_ipv6"`
	InstanceID          string `json:"instance_id"`
}

// configWritePayload is what POST /api/config accepts. TechnitiumToken is
// write-only and optional: a blank value means "leave the stored token
// unchanged", so the UI never needs to round-trip the real secret just to
// save an unrelated field.
type configWritePayload struct {
	TechnitiumBaseURL   string `json:"technitium_base_url"`
	TechnitiumToken     string `json:"technitium_token"`
	Zone                string `json:"zone"`
	TTLSeconds          int    `json:"ttl_seconds"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	LANIPv4             string `json:"lan_ipv4"`
	AAAAEnabled         bool   `json:"aaaa_enabled"`
	LANIPv6             string `json:"lan_ipv6"`
}

func toReadPayload(c config.Config) configReadPayload {
	return configReadPayload{
		TechnitiumBaseURL:   c.TechnitiumBaseURL,
		TechnitiumTokenSet:  c.TechnitiumToken != "",
		Zone:                c.Zone,
		TTLSeconds:          c.TTLSeconds,
		PollIntervalSeconds: c.PollIntervalSeconds,
		LANIPv4:             c.LANIPv4,
		AAAAEnabled:         c.AAAAEnabled,
		LANIPv6:             c.LANIPv6,
		InstanceID:          c.InstanceID,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// hasCSRFHeader reports whether the request carries a non-empty
// X-Zoraxy-Csrf header. This plugin has no access to Zoraxy's own signing
// secret, so it cannot cryptographically validate the token's value - but
// requiring the header to be present and non-empty on state-changing
// requests already defeats classic cross-origin form-based CSRF, since a
// plain HTML form cannot set a custom header, and a cross-origin fetch/XHR
// that tried to would need a CORS preflight this plugin never grants.
func hasCSRFHeader(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("X-Zoraxy-Csrf")) != ""
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, toReadPayload(s.Config.Snapshot()))

	case http.MethodPost:
		if !hasCSRFHeader(r) {
			writeError(w, http.StatusForbidden, "missing X-Zoraxy-Csrf header")
			return
		}
		var payload configWritePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		updated, err := s.Config.Update(func(c *config.Config) {
			c.TechnitiumBaseURL = payload.TechnitiumBaseURL
			if strings.TrimSpace(payload.TechnitiumToken) != "" {
				c.TechnitiumToken = payload.TechnitiumToken
			}
			c.Zone = payload.Zone
			c.TTLSeconds = payload.TTLSeconds
			c.PollIntervalSeconds = payload.PollIntervalSeconds
			c.LANIPv4 = payload.LANIPv4
			c.AAAAEnabled = payload.AAAAEnabled
			c.LANIPv6 = payload.LANIPv6
			// InstanceID is never accepted from the client.
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if s.Trigger != nil {
			select {
			case s.Trigger <- struct{}{}:
			default:
			}
		}
		writeJSON(w, http.StatusOK, toReadPayload(updated))

	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, s.Status.Get())
}
