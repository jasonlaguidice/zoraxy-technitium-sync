package config

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/svcparam"
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

// TestLoad_DefaultsHTTPSOffWithAutoHintsOn checks the fresh-config defaults
// for the HTTPS record settings: the toggle is off (opt-in like AAAA), the
// priority starts at 0 (alias mode) and both automatic-hint options default
// to on, mirroring Technitium's own "Automatic Hints" option.
func TestLoad_DefaultsHTTPSOffWithAutoHintsOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPSEnabled {
		t.Errorf("expected https toggle off by default")
	}
	if cfg.HTTPSPriorityValue() != 1 {
		t.Errorf("expected https priority 1 by default, got %d", cfg.HTTPSPriorityValue())
	}
	if cfg.HTTPSTargetName != "" {
		t.Errorf("expected blank https target name by default, got %q", cfg.HTTPSTargetName)
	}
	if len(cfg.HTTPSParams) != 0 {
		t.Errorf("expected no https params by default, got %+v", cfg.HTTPSParams)
	}
	if !cfg.AutoIPv4HintEnabled() || !cfg.AutoIPv6HintEnabled() {
		t.Errorf("expected automatic hints to default to on, got %v/%v", cfg.AutoIPv4HintEnabled(), cfg.AutoIPv6HintEnabled())
	}
}

// TestLoad_OldConfigWithoutHTTPSFieldsDefaultsAutoHintsOn covers the upgrade
// path: a config.json written by an older plugin version has no
// https_auto_ipv4_hint/https_auto_ipv6_hint fields at all, and those must
// default to on (the same default a fresh config gets) rather than to the
// zero value off.
func TestLoad_OldConfigWithoutHTTPSFieldsDefaultsAutoHintsOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{"technitium_base_url":"http://technitium.example:5380","zone":"example.org","ttl_seconds":300,"poll_interval_seconds":30,"lan_ipv4":"192.0.2.1","lan_ipv6":"2001:db8::1","instance_id":"x"}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPSEnabled {
		t.Errorf("expected https toggle off when absent from disk")
	}
	if !cfg.AutoIPv4HintEnabled() || !cfg.AutoIPv6HintEnabled() {
		t.Errorf("expected absent automatic hints to default to on, got %v/%v", cfg.AutoIPv4HintEnabled(), cfg.AutoIPv6HintEnabled())
	}
	if cfg.HTTPSPriorityValue() != 1 {
		t.Errorf("expected absent https priority to default to 1, got %d", cfg.HTTPSPriorityValue())
	}
}

// TestLoad_ExplicitZeroPriorityIsPreserved makes sure an explicitly stored
// priority 0 (alias mode) is not mistaken for "absent" and reset to 1.
func TestLoad_ExplicitZeroPriorityIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{"technitium_base_url":"http://technitium.example:5380","zone":"example.org","ttl_seconds":300,"poll_interval_seconds":30,"lan_ipv4":"192.0.2.1","instance_id":"x","https_priority":0}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPSPriorityValue() != 0 {
		t.Errorf("expected explicit priority 0 to be preserved, got %d", cfg.HTTPSPriorityValue())
	}
}

// TestValidate_HTTPSSettings covers the https-only validation rules, which
// apply only while the toggle is on.
func TestValidate_HTTPSSettings(t *testing.T) {
	// Off: everything is inert and must not block a save.
	cfg := validConfigForTest()
	cfg.HTTPSEnabled = false
	cfg.HTTPSPriority = intPtr(70000)
	cfg.HTTPSParams = []svcparam.Param{{Key: "bogus", Value: "x"}}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected https settings to be inert while disabled, got: %v", err)
	}

	// On: priority must stay in the uint16 range (0 = alias mode).
	cfg = validConfigForTest()
	cfg.HTTPSEnabled = true
	cfg.HTTPSPriority = intPtr(70000)
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for https priority above 65535")
	}
	cfg.HTTPSPriority = intPtr(-1)
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for negative https priority")
	}

	// On: each param must be a key/value pair Technitium would accept.
	cfg = validConfigForTest()
	cfg.HTTPSEnabled = true
	cfg.HTTPSParams = []svcparam.Param{{Key: "port", Value: "abc"}}
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for invalid port param")
	}
	cfg.HTTPSParams = []svcparam.Param{{Key: "alpn", Value: "a|b"}}
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for '|' in a param value")
	}

	// On: the same key twice (case-insensitively) is rejected, because
	// Technitium stores params as a dictionary.
	cfg = validConfigForTest()
	cfg.HTTPSEnabled = true
	cfg.HTTPSParams = []svcparam.Param{{Key: "alpn", Value: "h2"}, {Key: "ALPN", Value: "h3"}}
	if err := Validate(cfg); err == nil {
		t.Fatalf("expected error for duplicate param key")
	}

	// On: a valid combination passes.
	cfg = validConfigForTest()
	cfg.HTTPSEnabled = true
	cfg.HTTPSPriority = intPtr(5)
	cfg.HTTPSTargetName = "app.example.com"
	cfg.HTTPSParams = []svcparam.Param{
		{Key: "alpn", Value: "h2,h3"},
		{Key: "port", Value: "443"},
		{Key: "65", Value: "010203"},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestStore_UpdatePersistsHTTPSFields checks the https settings survive a
// save/reload round trip, including explicit off values for the automatic
// hints (which must not be "helpfully" reset to the default on).
func TestStore_UpdatePersistsHTTPSFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	store := NewStore(cfg, path)

	if _, err := store.Update(func(c *Config) {
		c.TechnitiumBaseURL = "http://technitium.example:5380"
		c.Zone = "example.org"
		c.LANIPv4 = "192.0.2.1"
		c.HTTPSEnabled = true
		c.HTTPSPriority = intPtr(5)
		c.HTTPSTargetName = "app.example.com"
		c.HTTPSParams = []svcparam.Param{
			{Key: "alpn", Value: "h2,h3"},
			{Key: "port", Value: "443"},
		}
		c.HTTPSAutoIPv4Hint = boolPtr(false)
		c.HTTPSAutoIPv6Hint = boolPtr(false)
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reloaded.HTTPSEnabled || reloaded.HTTPSPriorityValue() != 5 || reloaded.HTTPSTargetName != "app.example.com" {
		t.Fatalf("update was not persisted: %+v", reloaded)
	}
	if len(reloaded.HTTPSParams) != 2 || reloaded.HTTPSParams[0].Key != "alpn" || reloaded.HTTPSParams[0].Value != "h2,h3" {
		t.Fatalf("params were not persisted: %+v", reloaded.HTTPSParams)
	}
	if reloaded.AutoIPv4HintEnabled() || reloaded.AutoIPv6HintEnabled() {
		t.Fatalf("explicitly-off automatic hints must stay off across a reload")
	}
}
