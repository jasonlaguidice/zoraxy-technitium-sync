// Package technitium is a small CRUD client for Technitium DNS Server's HTTP
// API, plus the ownership-marker logic that makes it safe for
// zoraxy-technitium-sync to update or delete records it did not itself
// create. It implements internal/reconciler.DNSStore.
//
// Response shape note: Technitium's own API docs describe the request
// parameters precisely but not the exact JSON shape of
// /api/zones/records/get's "response" object. This client assumes the
// well-known Technitium shape (response.records[], each with name/type/ttl
// and a type-specific rData object such as {"ipAddress":"..."} or
// {"text":"..."}), which is what every Technitium release has shipped. It
// has not been verified against a live server in this environment; tests
// here exercise this client against a fake server built to that same
// assumed shape.
package technitium

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/reconciler"
)

const markerHeritage = "zoraxy-technitium-sync"
const markerPrefix = "_ztsync."

// ErrNotOwned is returned by the ownership-gated Update*/Delete* operations
// when the target hostname's TXT marker is missing or belongs to a different
// instance. Callers should treat it as a silent skip, not a failure.
var ErrNotOwned = errors.New("technitium: hostname is not owned by this plugin instance")

// Client talks to one Technitium DNS Server zone.
type Client struct {
	BaseURL    string
	Token      string
	Zone       string
	TTLSeconds int
	InstanceID string
	HTTPClient *http.Client
}

func New(baseURL, token, zone string, ttlSeconds int, instanceID string) *Client {
	return &Client{
		BaseURL:    baseURL,
		Token:      token,
		Zone:       zone,
		TTLSeconds: ttlSeconds,
		InstanceID: instanceID,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) markerValue() string {
	return fmt.Sprintf("heritage=%s,instance=%s", markerHeritage, c.InstanceID)
}

func markerDomain(hostname string) string {
	return markerPrefix + hostname
}

func hostnameFromMarkerDomain(domain string) (string, bool) {
	if !strings.HasPrefix(domain, markerPrefix) {
		return "", false
	}
	return strings.TrimPrefix(domain, markerPrefix), true
}

type apiEnvelope struct {
	Status            string          `json:"status"`
	Response          json.RawMessage `json:"response"`
	ErrorMessage      string          `json:"errorMessage"`
	StackTrace        string          `json:"stackTrace"`
	InnerErrorMessage string          `json:"innerErrorMessage"`
}

// isBenignConflict reports whether a Technitium error message indicates the
// record we tried to add already exists exactly as requested - a no-op, not
// a real failure.
func isBenignConflict(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "already exists") || strings.Contains(m, "identical record")
}

func (c *Client) do(ctx context.Context, path string, params url.Values) (json.RawMessage, error) {
	params.Set("token", c.Token)
	reqURL := strings.TrimRight(c.BaseURL, "/") + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling technitium %s: %w", path, err)
	}
	defer resp.Body.Close()

	var env apiEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decoding technitium %s response: %w", path, err)
	}
	if env.Status != "ok" {
		if isBenignConflict(env.ErrorMessage) {
			return env.Response, nil
		}
		return nil, fmt.Errorf("technitium %s: status=%s message=%s", path, env.Status, env.ErrorMessage)
	}
	return env.Response, nil
}

func (c *Client) addRecord(ctx context.Context, domain, recordType string, extra map[string]string) error {
	v := url.Values{}
	v.Set("zone", c.Zone)
	v.Set("domain", domain)
	v.Set("type", recordType)
	v.Set("ttl", strconv.Itoa(c.TTLSeconds))
	for k, val := range extra {
		v.Set(k, val)
	}
	_, err := c.do(ctx, "/api/zones/records/add", v)
	return err
}

func (c *Client) updateRecord(ctx context.Context, domain, recordType string, extra map[string]string) error {
	v := url.Values{}
	v.Set("zone", c.Zone)
	v.Set("domain", domain)
	v.Set("type", recordType)
	v.Set("ttl", strconv.Itoa(c.TTLSeconds))
	for k, val := range extra {
		v.Set(k, val)
	}
	_, err := c.do(ctx, "/api/zones/records/update", v)
	return err
}

func (c *Client) deleteRecord(ctx context.Context, domain, recordType string, extra map[string]string) error {
	v := url.Values{}
	v.Set("zone", c.Zone)
	v.Set("domain", domain)
	v.Set("type", recordType)
	for k, val := range extra {
		v.Set(k, val)
	}
	_, err := c.do(ctx, "/api/zones/records/delete", v)
	return err
}

func (c *Client) addA(ctx context.Context, hostname, ip string) error {
	return c.addRecord(ctx, hostname, "A", map[string]string{"ipAddress": ip})
}

func (c *Client) addAAAA(ctx context.Context, hostname, ip string) error {
	return c.addRecord(ctx, hostname, "AAAA", map[string]string{"ipAddress": ip})
}

func (c *Client) addTXT(ctx context.Context, domain, text string) error {
	return c.addRecord(ctx, domain, "TXT", map[string]string{"text": text})
}

func (c *Client) updateA(ctx context.Context, hostname, oldIP, newIP string) error {
	return c.updateRecord(ctx, hostname, "A", map[string]string{"ipAddress": oldIP, "newIpAddress": newIP})
}

func (c *Client) updateAAAA(ctx context.Context, hostname, oldIP, newIP string) error {
	return c.updateRecord(ctx, hostname, "AAAA", map[string]string{"ipAddress": oldIP, "newIpAddress": newIP})
}

func (c *Client) deleteA(ctx context.Context, hostname, ip string) error {
	return c.deleteRecord(ctx, hostname, "A", map[string]string{"ipAddress": ip})
}

func (c *Client) deleteAAAA(ctx context.Context, hostname, ip string) error {
	return c.deleteRecord(ctx, hostname, "AAAA", map[string]string{"ipAddress": ip})
}

func (c *Client) deleteTXT(ctx context.Context, domain, text string) error {
	return c.deleteRecord(ctx, domain, "TXT", map[string]string{"text": text})
}

// checkOwnership queries only the ownership marker's own domain (a cheap,
// single-name lookup, not a whole-zone dump) and reports whether it exists
// with a value matching this instance.
func (c *Client) checkOwnership(ctx context.Context, hostname string) (bool, error) {
	v := url.Values{}
	v.Set("zone", c.Zone)
	v.Set("domain", markerDomain(hostname))
	raw, err := c.do(ctx, "/api/zones/records/get", v)
	if err != nil {
		return false, err
	}
	records, err := parseRecords(raw)
	if err != nil {
		return false, err
	}
	want := c.markerValue()
	for _, r := range records {
		if strings.EqualFold(r.Type, "TXT") && r.Text == want {
			return true, nil
		}
	}
	return false, nil
}

// CreateHost adds the ownership marker (a benign no-op if it already exists
// with our own value) and the A record, plus an AAAA record if ipv6 is
// non-empty. It is used both for genuinely new hostnames and for the
// edge case where we already own the marker but the A record is missing.
func (c *Client) CreateHost(ctx context.Context, hostname, ipv4, ipv6 string) error {
	if err := c.addTXT(ctx, markerDomain(hostname), c.markerValue()); err != nil {
		return fmt.Errorf("creating ownership marker for %s: %w", hostname, err)
	}
	if err := c.addA(ctx, hostname, ipv4); err != nil {
		return fmt.Errorf("creating A record for %s: %w", hostname, err)
	}
	if ipv6 != "" {
		if err := c.addAAAA(ctx, hostname, ipv6); err != nil {
			return fmt.Errorf("creating AAAA record for %s: %w", hostname, err)
		}
	}
	return nil
}

// UpdateA re-verifies ownership immediately before updating, independent of
// whatever ownership state the caller's zone snapshot said earlier in the
// cycle.
func (c *Client) UpdateA(ctx context.Context, hostname, oldIP, newIP string) error {
	owned, err := c.checkOwnership(ctx, hostname)
	if err != nil {
		return err
	}
	if !owned {
		return ErrNotOwned
	}
	return c.updateA(ctx, hostname, oldIP, newIP)
}

func (c *Client) CreateAAAA(ctx context.Context, hostname, ip string) error {
	owned, err := c.checkOwnership(ctx, hostname)
	if err != nil {
		return err
	}
	if !owned {
		return ErrNotOwned
	}
	return c.addAAAA(ctx, hostname, ip)
}

func (c *Client) UpdateAAAA(ctx context.Context, hostname, oldIP, newIP string) error {
	owned, err := c.checkOwnership(ctx, hostname)
	if err != nil {
		return err
	}
	if !owned {
		return ErrNotOwned
	}
	return c.updateAAAA(ctx, hostname, oldIP, newIP)
}

func (c *Client) DeleteAAAAOnly(ctx context.Context, hostname, ip string) error {
	owned, err := c.checkOwnership(ctx, hostname)
	if err != nil {
		return err
	}
	if !owned {
		return ErrNotOwned
	}
	return c.deleteAAAA(ctx, hostname, ip)
}

// DeleteHost removes the A record, the AAAA record (if ip6 is non-empty) and
// finally the ownership marker itself, but only after confirming this
// instance owns the marker.
func (c *Client) DeleteHost(ctx context.Context, hostname, ip4, ip6 string) error {
	owned, err := c.checkOwnership(ctx, hostname)
	if err != nil {
		return err
	}
	if !owned {
		return ErrNotOwned
	}
	if ip4 != "" {
		if err := c.deleteA(ctx, hostname, ip4); err != nil {
			return fmt.Errorf("deleting A record for %s: %w", hostname, err)
		}
	}
	if ip6 != "" {
		if err := c.deleteAAAA(ctx, hostname, ip6); err != nil {
			return fmt.Errorf("deleting AAAA record for %s: %w", hostname, err)
		}
	}
	if err := c.deleteTXT(ctx, markerDomain(hostname), c.markerValue()); err != nil {
		return fmt.Errorf("deleting ownership marker for %s: %w", hostname, err)
	}
	return nil
}

type parsedRecord struct {
	Name string
	Type string
	IP   string
	Text string
}

type rawRecordsResponse struct {
	Records []rawRecord `json:"records"`
}

type rawRecord struct {
	Name  string          `json:"name"`
	Type  string          `json:"type"`
	RData json.RawMessage `json:"rData"`
}

type rawRDataAddress struct {
	IPAddress string `json:"ipAddress"`
}

type rawRDataText struct {
	Text string `json:"text"`
}

func parseRecords(raw json.RawMessage) ([]parsedRecord, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var resp rawRecordsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parsing technitium records response: %w", err)
	}
	out := make([]parsedRecord, 0, len(resp.Records))
	for _, r := range resp.Records {
		pr := parsedRecord{Name: r.Name, Type: r.Type}
		switch strings.ToUpper(r.Type) {
		case "A", "AAAA":
			var addr rawRDataAddress
			if len(r.RData) > 0 {
				json.Unmarshal(r.RData, &addr)
			}
			pr.IP = addr.IPAddress
		case "TXT":
			var txt rawRDataText
			if len(r.RData) > 0 {
				json.Unmarshal(r.RData, &txt)
			}
			pr.Text = txt.Text
		}
		out = append(out, pr)
	}
	return out, nil
}

// GetZoneState implements reconciler.DNSStore: it dumps the entire zone in
// one call (also how ownership is recovered after a restart - there is no
// separate in-memory "known hosts" state, this is always re-derived live)
// and groups A/AAAA/marker-TXT records by hostname.
func (c *Client) GetZoneState(ctx context.Context) (reconciler.ZoneState, error) {
	v := url.Values{}
	v.Set("zone", c.Zone)
	v.Set("domain", c.Zone)
	v.Set("listZone", "true")
	raw, err := c.do(ctx, "/api/zones/records/get", v)
	if err != nil {
		return reconciler.ZoneState{}, fmt.Errorf("listing zone records: %w", err)
	}
	records, err := parseRecords(raw)
	if err != nil {
		return reconciler.ZoneState{}, err
	}

	hosts := map[string]reconciler.HostRecords{}
	// Technitium allows multiple TXT records at the same name, so track
	// "does at least one of them match our marker" rather than the value of
	// whichever record happens to appear last in the response - a plain
	// overwrite here could let a stray foreign TXT at the same _ztsync name
	// mask our own real marker depending on response order, disagreeing with
	// checkOwnership (which scans all of them) and causing this snapshot to
	// wrongly report a host we actually own as unowned.
	markerMatched := map[string]bool{}
	want := c.markerValue()
	for _, r := range records {
		name := strings.ToLower(strings.TrimSuffix(r.Name, "."))
		switch strings.ToUpper(r.Type) {
		case "A":
			hr := hosts[name]
			hr.AIPs = append(hr.AIPs, r.IP)
			hosts[name] = hr
		case "AAAA":
			hr := hosts[name]
			hr.AAAAIPs = append(hr.AAAAIPs, r.IP)
			hosts[name] = hr
		case "TXT":
			if hostname, ok := hostnameFromMarkerDomain(name); ok {
				if _, seen := markerMatched[hostname]; !seen {
					markerMatched[hostname] = false
				}
				if r.Text == want {
					markerMatched[hostname] = true
				}
			}
		}
	}
	for hostname, matched := range markerMatched {
		hr := hosts[hostname]
		hr.OwnedByUs = matched
		hosts[hostname] = hr
	}
	return reconciler.ZoneState{Hosts: hosts}, nil
}
