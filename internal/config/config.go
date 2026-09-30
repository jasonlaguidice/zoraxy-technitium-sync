// Package config persists zoraxy-technitium-sync's own settings (Technitium
// connection details, the managed zone, TTL, poll interval and the AAAA
// toggle) to a flat JSON file next to the plugin binary. Zoraxy's own
// ConfigureSpec (port, API key, Zoraxy's port) is unrelated runtime wiring
// and is not stored here.
package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/svcparam"
)

const (
	DefaultTTLSeconds          = 300
	DefaultPollIntervalSeconds = 30

	MinTTLSeconds          = 30
	MinPollIntervalSeconds = 5
)

// Config holds every user-editable setting for this plugin. It is the only
// state that must survive a restart in memory-independent form (DNS record
// ownership itself is re-derived from Technitium on every reconcile cycle,
// not cached here).
type Config struct {
	TechnitiumBaseURL   string `json:"technitium_base_url"`
	TechnitiumToken     string `json:"technitium_token"`
	Zone                string `json:"zone"`
	TTLSeconds          int    `json:"ttl_seconds"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	LANIPv4             string `json:"lan_ipv4"`
	AAAAEnabled         bool   `json:"aaaa_enabled"`
	LANIPv6             string `json:"lan_ipv6"`
	// InstanceID identifies this plugin instance in the TXT ownership marker
	// (heritage=zoraxy-technitium-sync,instance=<InstanceID>). Generated once
	// on first run and then persisted; it must stay stable across restarts or
	// this instance will stop recognising records it created before.
	InstanceID string `json:"instance_id"`

	// HTTPS record settings. The HTTPS (RFC 9460) record is optional, like
	// the AAAA record: HTTPSEnabled is a global toggle that, when on, makes
	// every managed hostname also get one HTTPS record built from the fields
	// below. Priority 0 means alias mode; anything above 0 is service mode
	// (absent defaults to 1).
	// TargetName is the record's target domain name; blank means "." (the
	// zone root). Params are the service binding parameters, sent to
	// Technitium verbatim as svcParams key/value pairs. The auto-hint flags
	// mirror Technitium's "Automatic Hints" option: when on, Technitium
	// resolves the ipv4hint/ipv6hint param itself from the target name's
	// A/AAAA records and keeps it refreshed whenever those records change.
	// Priority and the auto-hint flags are pointers so that a config written
	// by an older plugin version (no such fields on disk) gets the same
	// defaults as a fresh config (priority 1, hints on) while an explicit 0
	// or false is still preserved.
	HTTPSEnabled      bool             `json:"https_enabled"`
	HTTPSPriority     *int             `json:"https_priority"`
	HTTPSTargetName   string           `json:"https_target_name"`
	HTTPSParams       []svcparam.Param `json:"https_params"`
	HTTPSAutoIPv4Hint *bool            `json:"https_auto_ipv4_hint"`
	HTTPSAutoIPv6Hint *bool            `json:"https_auto_ipv6_hint"`
}

// defaultHTTPSPriority is the service-mode priority used when none is set.
const defaultHTTPSPriority = 1

// HTTPSPriorityValue returns the configured HTTPS priority, or the default
// when none is stored.
func (c *Config) HTTPSPriorityValue() int {
	if c.HTTPSPriority == nil {
		return defaultHTTPSPriority
	}
	return *c.HTTPSPriority
}

// AutoIPv4HintEnabled reports whether Technitium's Automatic Hints option is
// enabled for the ipv4hint param. Absent (nil) means on: both a fresh config
// and a config file written before this option existed default to on.
func (c *Config) AutoIPv4HintEnabled() bool {
	return c.HTTPSAutoIPv4Hint == nil || *c.HTTPSAutoIPv4Hint
}

// AutoIPv6HintEnabled is AutoIPv4HintEnabled for the ipv6hint param.
func (c *Config) AutoIPv6HintEnabled() bool {
	return c.HTTPSAutoIPv6Hint == nil || *c.HTTPSAutoIPv6Hint
}

func defaultConfig() *Config {
	return &Config{
		// Technitium base URL and zone have no sane default for a plugin
		// someone else installs: they're specific to the operator's own DNS
		// server. Left blank, Validate rejects saving until the user fills
		// them in via the UI, and the reconcile loop just logs (non-fatally)
		// until then.
		TechnitiumBaseURL:   "",
		Zone:                "",
		TTLSeconds:          DefaultTTLSeconds,
		PollIntervalSeconds: DefaultPollIntervalSeconds,
		// Unlike the Technitium URL/zone, the LAN targets DO have a sane
		// default: this plugin always runs on the Zoraxy box itself, so that
		// box's own outbound-facing address is the right address for every
		// managed record to point at. Detected once at first-run; blank
		// (same as before) if detection fails.
		LANIPv4:     detectLocalIPv4(),
		AAAAEnabled: false,
		LANIPv6:     detectLocalIPv6(),
		InstanceID:  newInstanceID(),
		// HTTPS records are opt-in like AAAA records (off by default), but
		// Technitium's Automatic Hints default to on wherever the option
		// exists at all.
		HTTPSEnabled:      false,
		HTTPSPriority:     intPtr(defaultHTTPSPriority),
		HTTPSTargetName:   "",
		HTTPSParams:       nil,
		HTTPSAutoIPv4Hint: boolPtr(true),
		HTTPSAutoIPv6Hint: boolPtr(true),
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func intPtr(n int) *int {
	return &n
}

// detectLocalIP returns the local address the OS routing table would use to
// reach dialTarget, or "" if that can't be determined. Dialing UDP performs
// no handshake and transmits nothing — it's just a local socket + route
// lookup — so this is fast and safe to call at startup even without real
// internet connectivity, as long as a route exists.
func detectLocalIP(network, dialTarget string) string {
	conn, err := net.Dial(network, dialTarget)
	if err != nil {
		return ""
	}
	defer conn.Close()
	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	return udpAddr.IP.String()
}

func detectLocalIPv4() string { return detectLocalIP("udp4", "8.8.8.8:80") }

// detectLocalIPv6 is platform-specific: see detect_ipv6_linux.go and
// detect_ipv6_other.go. The UDP-dial trick used above for IPv4 has no
// privacy-extension equivalent to worry about, but on Linux it picks
// whatever address outbound routing prefers, which by default is a
// temporary/privacy address (RFC 4941) that rotates — the wrong thing to
// point a DNS record at.

func newInstanceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is effectively unrecoverable on any real system;
		// fall back to a fixed-but-valid UUIDv4 shape rather than crashing.
		copy(b[:], []byte("zoraxytechnitium"))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func clone(c *Config) *Config {
	cp := *c
	return &cp
}

func clampDefaults(c *Config) {
	// TechnitiumBaseURL and Zone are intentionally left alone here, blank or
	// not: there is no default to fall back to, so a blank value is
	// respected rather than overwritten (Validate is what stops a blank
	// value from being saved going forward).
	if c.TTLSeconds < MinTTLSeconds {
		c.TTLSeconds = DefaultTTLSeconds
	}
	if c.PollIntervalSeconds < MinPollIntervalSeconds {
		c.PollIntervalSeconds = DefaultPollIntervalSeconds
	}
	if c.LANIPv4 == "" {
		// Re-attempt detection rather than leaving it blank: cheap,
		// local-only, and gives a config that somehow ended up without a
		// LAN target a chance to self-heal on every Load.
		c.LANIPv4 = detectLocalIPv4()
	}
	// LANIPv6 is deliberately NOT re-detected here: it's optional, detected
	// once at first-run creation time, and otherwise left exactly as stored
	// (including intentionally blank) — Validate already enforces it being
	// non-empty when AAAAEnabled is on.
	if c.InstanceID == "" {
		c.InstanceID = newInstanceID()
	}
}

// Load reads the config file at path, creating it with defaults (and a fresh
// InstanceID) if it does not exist yet.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := defaultConfig()
		if err := save(cfg, path); err != nil {
			return nil, fmt.Errorf("writing default config: %w", err)
		}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	clampDefaults(&cfg)
	return &cfg, nil
}

// save writes the config atomically: temp file, fsync, then rename, so a
// crash mid-write cannot leave a corrupt config.json behind.
func save(c *Config, path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// Store is the thread-safe, persisted holder of the live config. All reads
// return a copy so callers never need to hold a lock while using the value.
type Store struct {
	mu   sync.RWMutex
	cfg  *Config
	path string
}

func NewStore(cfg *Config, path string) *Store {
	return &Store{cfg: cfg, path: path}
}

func (s *Store) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *clone(s.cfg)
}

// Update applies fn to a copy of the current config, validates and persists
// the result, and only then swaps it in. fn should mutate the passed Config
// in place.
func (s *Store) Update(fn func(*Config)) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.cfg)
	fn(next)
	if err := Validate(next); err != nil {
		return Config{}, err
	}
	if err := save(next, s.path); err != nil {
		return Config{}, err
	}
	s.cfg = next
	return *clone(next), nil
}

// Validate rejects settings that would make the plugin misbehave rather than
// simply fail loudly at the next Technitium call.
func Validate(c *Config) error {
	if c.TechnitiumBaseURL == "" {
		return errors.New("technitium base url is required")
	}
	if c.Zone == "" {
		return errors.New("zone is required")
	}
	if c.TTLSeconds < MinTTLSeconds {
		return fmt.Errorf("ttl must be >= %d seconds", MinTTLSeconds)
	}
	if c.PollIntervalSeconds < MinPollIntervalSeconds {
		return fmt.Errorf("poll interval must be >= %d seconds", MinPollIntervalSeconds)
	}
	if c.LANIPv4 == "" {
		return errors.New("lan ipv4 target is required")
	}
	if c.AAAAEnabled && c.LANIPv6 == "" {
		return errors.New("lan ipv6 target is required when AAAA is enabled")
	}
	// HTTPS settings are only validated while the toggle is on: when it is
	// off they are inert, and rejecting a save because of a value nobody is
	// using would just block unrelated edits.
	if c.HTTPSEnabled {
		if p := c.HTTPSPriorityValue(); p < 0 || p > 65535 {
			return fmt.Errorf("https priority must be between 0 and 65535 (0 = alias mode), got %d", p)
		}
		seen := make(map[string]bool, len(c.HTTPSParams))
		for _, p := range c.HTTPSParams {
			if err := svcparam.ValidateParam(p.Key, p.Value); err != nil {
				return err
			}
			k, _ := svcparam.NormalizeKey(p.Key)
			if seen[k] {
				return fmt.Errorf("HTTPS param %s appears more than once", k)
			}
			seen[k] = true
		}
	}
	return nil
}
