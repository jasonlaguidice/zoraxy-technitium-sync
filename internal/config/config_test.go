package config

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_CreatesDefaultConfigWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Technitium base URL and zone are specific to the operator's own DNS
	// server: there is no sane default, so a fresh config must leave them
	// blank rather than baking in someone's personal test values.
	if cfg.TechnitiumBaseURL != "" {
		t.Errorf("expected blank technitium base url by default, got %q", cfg.TechnitiumBaseURL)
	}
	if cfg.Zone != "" {
		t.Errorf("expected blank zone by default, got %q", cfg.Zone)
	}
	if cfg.TTLSeconds != DefaultTTLSeconds {
		t.Errorf("expected default ttl, got %d", cfg.TTLSeconds)
	}
	if cfg.PollIntervalSeconds != DefaultPollIntervalSeconds {
		t.Errorf("expected default poll interval, got %d", cfg.PollIntervalSeconds)
	}
	// LANIPv4/LANIPv6 are auto-detected from whatever network the test
	// runner has, so the exact value isn't deterministic — only that it's
	// either a valid IP (detection succeeded) or blank (it didn't).
	if cfg.LANIPv4 != "" && net.ParseIP(cfg.LANIPv4) == nil {
		t.Errorf("expected lan ipv4 to be blank or a valid IP, got %q", cfg.LANIPv4)
	}
	if cfg.LANIPv6 != "" && net.ParseIP(cfg.LANIPv6) == nil {
		t.Errorf("expected lan ipv6 to be blank or a valid IP, got %q", cfg.LANIPv6)
	}
	if cfg.InstanceID == "" {
		t.Errorf("expected a generated instance id")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected config file to be written: %v", err)
	}
}

func TestLoad_PreservesInstanceIDAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	first, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	second, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first.InstanceID != second.InstanceID {
		t.Fatalf("instance id must be stable across restarts: %q vs %q", first.InstanceID, second.InstanceID)
	}
}

func TestStore_UpdatePersistsAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	store := NewStore(cfg, path)

	// TechnitiumBaseURL and LANIPv4 have to be supplied here too: a fresh
	// default config no longer ships a valid Technitium URL (there's no
	// sane default), and LANIPv4 isn't guaranteed to auto-detect on every
	// test runner, so the update itself must make the config valid.
	if _, err := store.Update(func(c *Config) {
		c.TechnitiumBaseURL = "http://technitium.example:5380"
		c.Zone = "example.org"
		c.TTLSeconds = 600
		c.LANIPv4 = "192.0.2.1"
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reloaded.Zone != "example.org" || reloaded.TTLSeconds != 600 {
		t.Fatalf("update was not persisted: %+v", reloaded)
	}

	// No leftover temp file after a successful save.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("expected temp file to be cleaned up, stat err: %v", err)
	}
}

func TestStore_UpdateRejectsInvalidValueAndKeepsOldOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	store := NewStore(cfg, path)

	// Seed a fully valid config first. A fresh default config no longer
	// passes Validate on its own (Technitium URL/zone are blank until the
	// user fills them in, and LANIPv4 isn't guaranteed to auto-detect on
	// every test runner), and this test needs a known-good baseline to
	// prove a rejected update doesn't disturb it.
	const validZone = "example.org"
	if _, err := store.Update(func(c *Config) {
		c.TechnitiumBaseURL = "http://technitium.example:5380"
		c.Zone = validZone
		c.LANIPv4 = "192.0.2.1"
	}); err != nil {
		t.Fatalf("unexpected error seeding a valid config: %v", err)
	}

	_, err = store.Update(func(c *Config) {
		c.Zone = ""
	})
	if err == nil {
		t.Fatalf("expected validation error for empty zone")
	}

	snap := store.Snapshot()
	if snap.Zone != validZone {
		t.Fatalf("rejected update must not mutate the live config: %+v", snap)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var onDisk Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if onDisk.Zone != validZone {
		t.Fatalf("rejected update must not be written to disk: %+v", onDisk)
	}
}

// validConfigForTest returns a config that passes Validate on its own,
// independent of what (if anything) the test runner's network auto-detects
// for LANIPv4 and independent of the Technitium URL/zone now having no
// built-in default — so tests that aren't specifically about those fields
// can isolate the one thing they're actually checking.
func validConfigForTest() *Config {
	cfg := defaultConfig()
	cfg.TechnitiumBaseURL = "http://technitium.example:5380"
	cfg.Zone = "example.org"
	cfg.LANIPv4 = "192.0.2.1"
	return cfg
}

func TestValidate_RequiresIPv6WhenAAAAEnabled(t *testing.T) {
	cfg := validConfigForTest()
	cfg.AAAAEnabled = true
	cfg.LANIPv6 = ""
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected validation error when AAAA is enabled without an ipv6 target")
	}
	cfg.LANIPv6 = "fd00::1"
	if err := Validate(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_RejectsShortTTLAndPollInterval(t *testing.T) {
	cfg := validConfigForTest()
	cfg.TTLSeconds = 1
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for too-short ttl")
	}
	cfg = validConfigForTest()
	cfg.PollIntervalSeconds = 1
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for too-short poll interval")
	}
}

func TestValidate_RequiresTechnitiumBaseURLAndZone(t *testing.T) {
	cfg := validConfigForTest()
	cfg.TechnitiumBaseURL = ""
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for blank technitium base url")
	}

	cfg = validConfigForTest()
	cfg.Zone = ""
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for blank zone")
	}
}

func TestValidate_RequiresLANIPv4(t *testing.T) {
	cfg := validConfigForTest()
	cfg.LANIPv4 = ""
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for blank lan ipv4")
	}
}
