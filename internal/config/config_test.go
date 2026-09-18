package config

import (
	"encoding/json"
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
	if cfg.TechnitiumBaseURL != DefaultTechnitiumBaseURL {
		t.Errorf("expected default base url, got %q", cfg.TechnitiumBaseURL)
	}
	if cfg.Zone != DefaultZone {
		t.Errorf("expected default zone, got %q", cfg.Zone)
	}
	if cfg.TTLSeconds != DefaultTTLSeconds {
		t.Errorf("expected default ttl, got %d", cfg.TTLSeconds)
	}
	if cfg.PollIntervalSeconds != DefaultPollIntervalSeconds {
		t.Errorf("expected default poll interval, got %d", cfg.PollIntervalSeconds)
	}
	if cfg.LANIPv4 != DefaultLANIPv4 {
		t.Errorf("expected default lan ipv4, got %q", cfg.LANIPv4)
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

	if _, err := store.Update(func(c *Config) {
		c.Zone = "example.org"
		c.TTLSeconds = 600
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

	_, err = store.Update(func(c *Config) {
		c.Zone = ""
	})
	if err == nil {
		t.Fatalf("expected validation error for empty zone")
	}

	snap := store.Snapshot()
	if snap.Zone != DefaultZone {
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
	if onDisk.Zone != DefaultZone {
		t.Fatalf("rejected update must not be written to disk: %+v", onDisk)
	}
}

func TestValidate_RequiresIPv6WhenAAAAEnabled(t *testing.T) {
	cfg := defaultConfig()
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
	cfg := defaultConfig()
	cfg.TTLSeconds = 1
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for too-short ttl")
	}
	cfg = defaultConfig()
	cfg.PollIntervalSeconds = 1
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for too-short poll interval")
	}
}
