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
// {"text":"..."}; HTTPS records additionally carry svcPriority,
// svcTargetName, a svcParams key/value object and the autoIpv4Hint/
// autoIpv6Hint flags), which is what every Technitium release has shipped.
// It has not been verified against a live server in this environment; tests
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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/reconciler"
	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/svcparam"
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

// svcTargetNameParam sends an SVCB target name the way Technitium expects:
// Technitium trims trailing dots, so "." and "" both arrive as the empty
// target (the zone root); sending "." for blank keeps the request shape
// matching what Technitium's own web console sends.
func svcTargetNameParam(target string) string {
	if strings.TrimSpace(target) == "" {
		return "."
	}
	return target
}

// svcParamsParam encodes a param list into Technitium's pipe-separated
// svcParams form, or the literal "false" when there are none (how Technitium
// spells an empty parameter list).
func svcParamsParam(params []svcparam.Param) string {
	if encoded, ok := svcparam.EncodeParams(params); ok {
		return encoded
	}
	return "false"
}

func boolParam(b bool) string {
	return strconv.FormatBool(b)
}

// updateSvcbHintsParam asks Technitium to refresh the Automatic Hints of any
// SVCB/HTTPS record in the zone whose target name matches the A/AAAA record
// being mutated - without it, automatic hints would go stale whenever the
// LAN targets change.
const updateSvcbHintsParam = "true"

func (c *Client) addA(ctx context.Context, hostname, ip string) error {
	return c.addRecord(ctx, hostname, "A", map[string]string{"ipAddress": ip, "updateSvcbHints": updateSvcbHintsParam})
}

func (c *Client) addAAAA(ctx context.Context, hostname, ip string) error {
	return c.addRecord(ctx, hostname, "AAAA", map[string]string{"ipAddress": ip, "updateSvcbHints": updateSvcbHintsParam})
}

func (c *Client) addTXT(ctx context.Context, domain, text string) error {
	return c.addRecord(ctx, domain, "TXT", map[string]string{"text": text})
}

func (c *Client) updateA(ctx context.Context, hostname, oldIP, newIP string) error {
	return c.updateRecord(ctx, hostname, "A", map[string]string{"ipAddress": oldIP, "newIpAddress": newIP, "updateSvcbHints": updateSvcbHintsParam})
}

func (c *Client) updateAAAA(ctx context.Context, hostname, oldIP, newIP string) error {
	return c.updateRecord(ctx, hostname, "AAAA", map[string]string{"ipAddress": oldIP, "newIpAddress": newIP, "updateSvcbHints": updateSvcbHintsParam})
}

func (c *Client) deleteA(ctx context.Context, hostname, ip string) error {
	return c.deleteRecord(ctx, hostname, "A", map[string]string{"ipAddress": ip, "updateSvcbHints": updateSvcbHintsParam})
}

func (c *Client) deleteAAAA(ctx context.Context, hostname, ip string) error {
	return c.deleteRecord(ctx, hostname, "AAAA", map[string]string{"ipAddress": ip, "updateSvcbHints": updateSvcbHintsParam})
}

func (c *Client) deleteTXT(ctx context.Context, domain, text string) error {
	return c.deleteRecord(ctx, domain, "TXT", map[string]string{"text": text})
}

func (c *Client) addHTTPS(ctx context.Context, hostname string, spec reconciler.HTTPSSpec) error {
	return c.addRecord(ctx, hostname, "HTTPS", map[string]string{
		"svcPriority":   strconv.Itoa(spec.Priority),
		"svcTargetName": svcTargetNameParam(spec.TargetName),
		"svcParams":     svcParamsParam(spec.Params),
		"autoIpv4Hint":  boolParam(spec.AutoIPv4Hint),
		"autoIpv6Hint":  boolParam(spec.AutoIPv6Hint),
	})
}

func (c *Client) updateHTTPS(ctx context.Context, hostname string, current, desired reconciler.HTTPSSpec) error {
	return c.updateRecord(ctx, hostname, "HTTPS", map[string]string{
		// Technitium identifies the record being rewritten by its current
		// priority, target name and params; those must round-trip verbatim.
		"svcPriority":      strconv.Itoa(current.Priority),
		"svcTargetName":    svcTargetNameParam(current.TargetName),
		"svcParams":        svcParamsParam(current.Params),
		"newSvcPriority":   strconv.Itoa(desired.Priority),
		"newSvcTargetName": svcTargetNameParam(desired.TargetName),
		"newSvcParams":     svcParamsParam(desired.Params),
		// The auto flags apply to the new record; sending false explicitly
		// clears a previously-enabled Automatic Hints flag.
		"autoIpv4Hint": boolParam(desired.AutoIPv4Hint),
		"autoIpv6Hint": boolParam(desired.AutoIPv6Hint),
	})
}

func (c *Client) deleteHTTPS(ctx context.Context, hostname string, current reconciler.HTTPSSpec) error {
	return c.deleteRecord(ctx, hostname, "HTTPS", map[string]string{
		"svcPriority":   strconv.Itoa(current.Priority),
		"svcTargetName": svcTargetNameParam(current.TargetName),
		"svcParams":     svcParamsParam(current.Params),
	})
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
// non-empty and an HTTPS record if https is non-empty (at most one is
// supported). It is used both for genuinely new hostnames and for the
// edge case where we already own the marker but the A record is missing.
func (c *Client) CreateHost(ctx context.Context, hostname, ipv4, ipv6 string, https []reconciler.HTTPSSpec) error {
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
	if len(https) > 0 {
		if err := c.addHTTPS(ctx, hostname, https[0]); err != nil {
			return fmt.Errorf("creating HTTPS record for %s: %w", hostname, err)
		}
	}
	return nil
}

// CreateHTTPS adds the desired HTTPS record for a hostname whose ownership
// marker already exists (as CreateHost establishes first).
func (c *Client) CreateHTTPS(ctx context.Context, hostname string, spec reconciler.HTTPSSpec) error {
	owned, err := c.checkOwnership(ctx, hostname)
	if err != nil {
		return err
	}
	if !owned {
		return ErrNotOwned
	}
	return c.addHTTPS(ctx, hostname, spec)
}

// UpdateHTTPS re-verifies ownership immediately before updating, independent
// of whatever ownership state the caller's zone snapshot said earlier in the
// cycle.
func (c *Client) UpdateHTTPS(ctx context.Context, hostname string, current, desired reconciler.HTTPSSpec) error {
	owned, err := c.checkOwnership(ctx, hostname)
	if err != nil {
		return err
	}
	if !owned {
		return ErrNotOwned
	}
	return c.updateHTTPS(ctx, hostname, current, desired)
}

func (c *Client) DeleteHTTPSOnly(ctx context.Context, hostname string, current reconciler.HTTPSSpec) error {
	owned, err := c.checkOwnership(ctx, hostname)
	if err != nil {
		return err
	}
	if !owned {
		return ErrNotOwned
	}
	return c.deleteHTTPS(ctx, hostname, current)
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

// DeleteHost removes the first HTTPS record (if https is non-empty), the A
// record, the AAAA record (if ip6 is non-empty) and finally the ownership marker
// itself, but only after confirming this instance owns the marker. Like the
// A/AAAA handling, extra HTTPS records beyond the first are left untouched.
func (c *Client) DeleteHost(ctx context.Context, hostname, ip4, ip6 string, https []reconciler.HTTPSSpec) error {
	owned, err := c.checkOwnership(ctx, hostname)
	if err != nil {
		return err
	}
	if !owned {
		return ErrNotOwned
	}
	// The HTTPS record goes first: deleting the A/AAAA records (sent with
	// updateSvcbHints=true) would rewrite its hint params, and Technitium
	// identifies the record to delete by its current params.
	if len(https) > 0 {
		if err := c.deleteHTTPS(ctx, hostname, https[0]); err != nil {
			return fmt.Errorf("deleting HTTPS record for %s: %w", hostname, err)
		}
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

	// HTTPS-only fields, set when Type is "HTTPS".
	Priority     int
	TargetName   string
	Params       []svcparam.Param
	AutoIPv4Hint bool
	AutoIPv6Hint bool
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

// rawRDataHTTPS mirrors Technitium's rData object for SVCB/HTTPS records:
// svcPriority and svcTargetName, a svcParams object whose keys are the
// lowercase-dashed RFC key names (or decimal key codes for unknown keys) and
// whose values are the param values as strings, plus the Automatic Hints
// flags (always present on an authoritative zone listing).
type rawRDataHTTPS struct {
	SVCPriority   int               `json:"svcPriority"`
	SVCTargetName string            `json:"svcTargetName"`
	SVCParams     map[string]string `json:"svcParams"`
	AutoIpv4Hint  bool              `json:"autoIpv4Hint"`
	AutoIpv6Hint  bool              `json:"autoIpv6Hint"`
}

// lessParamKey orders param keys deterministically (canonical key first,
// raw lowercased spelling as a fallback) so params parsed from a map come
// back in a stable order.
func lessParamKey(a, b string) bool {
	ka, okA := svcparam.NormalizeKey(a)
	kb, okB := svcparam.NormalizeKey(b)
	if okA && okB {
		return ka < kb
	}
	return strings.ToLower(a) < strings.ToLower(b)
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
		case "HTTPS":
			var svcb rawRDataHTTPS
			if len(r.RData) > 0 {
				json.Unmarshal(r.RData, &svcb)
			}
			pr.Priority = svcb.SVCPriority
			pr.TargetName = svcb.SVCTargetName
			pr.AutoIPv4Hint = svcb.AutoIpv4Hint
			pr.AutoIPv6Hint = svcb.AutoIpv6Hint
			keys := make([]string, 0, len(svcb.SVCParams))
			for k := range svcb.SVCParams {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return lessParamKey(keys[i], keys[j]) })
			for _, k := range keys {
				pr.Params = append(pr.Params, svcparam.Param{Key: k, Value: svcb.SVCParams[k]})
			}
		}
		out = append(out, pr)
	}
	return out, nil
}

// GetZoneState implements reconciler.DNSStore: it dumps the entire zone in
// one call (also how ownership is recovered after a restart - there is no
// separate in-memory "known hosts" state, this is always re-derived live)
// and groups A/AAAA/HTTPS/marker-TXT records by hostname.
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
		case "HTTPS":
			hr := hosts[name]
			hr.HTTPSSpecs = append(hr.HTTPSSpecs, reconciler.HTTPSSpec{
				Priority:     r.Priority,
				TargetName:   r.TargetName,
				Params:       r.Params,
				AutoIPv4Hint: r.AutoIPv4Hint,
				AutoIPv6Hint: r.AutoIPv6Hint,
			})
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
