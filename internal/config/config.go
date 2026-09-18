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
	"os"
	"path/filepath"
	"sync"
)

const (
	DefaultTechnitiumBaseURL   = "http://192.168.1.254:5380"
	DefaultZone                = "shadowdrake.org"
	DefaultTTLSeconds          = 300
	DefaultPollIntervalSeconds = 30
	DefaultLANIPv4             = "192.168.1.24"

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
}

func defaultConfig() *Config {
	return &Config{
		TechnitiumBaseURL:   DefaultTechnitiumBaseURL,
		Zone:                DefaultZone,
		TTLSeconds:          DefaultTTLSeconds,
		PollIntervalSeconds: DefaultPollIntervalSeconds,
		LANIPv4:             DefaultLANIPv4,
		AAAAEnabled:         false,
		LANIPv6:             "",
		InstanceID:          newInstanceID(),
	}
}

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
	if c.TechnitiumBaseURL == "" {
		c.TechnitiumBaseURL = DefaultTechnitiumBaseURL
	}
	if c.Zone == "" {
		c.Zone = DefaultZone
	}
	if c.TTLSeconds < MinTTLSeconds {
		c.TTLSeconds = DefaultTTLSeconds
	}
	if c.PollIntervalSeconds < MinPollIntervalSeconds {
		c.PollIntervalSeconds = DefaultPollIntervalSeconds
	}
	if c.LANIPv4 == "" {
		c.LANIPv4 = DefaultLANIPv4
	}
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
	return nil
}
