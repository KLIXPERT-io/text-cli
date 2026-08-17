package config

import (
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/output"
)

// config.validOutput and output.Valid are two independent lists, because
// internal/config must not import internal/output at runtime. They drift
// silently: adding a format to output.Valid without adding it here makes
// `text config set defaults.output <fmt>` reject a format that --output
// accepts. This test is the only thing that catches that.
func TestValidOutputMatchesOutputPackage(t *testing.T) {
	formats := []string{"json", "toon", "ndjson", "csv", "table", "text"}
	for _, f := range formats {
		if !output.Valid(f) {
			t.Errorf("output.Valid(%q) = false; this test's list is stale", f)
		}
		if !validOutput(f) {
			t.Errorf("config.validOutput(%q) = false, but --output accepts it", f)
		}
	}
	for _, f := range []string{"yaml", "xml", "protobuf", ""} {
		if validOutput(f) {
			t.Errorf("config.validOutput(%q) = true, want false", f)
		}
	}
}

func TestTTLDefaults(t *testing.T) {
	c := Default()
	if got := c.TTL(); got.Hours() != 24 {
		t.Errorf("TTL() = %v, want 24h", got)
	}
	// A malformed duration must fall back rather than yield a zero TTL, which
	// would make every cache entry expire instantly.
	c.Cache.DefaultTTL = "not-a-duration"
	if got := c.TTL(); got.Hours() != 24 {
		t.Errorf("TTL() with a bad value = %v, want the 24h fallback", got)
	}
	c.Cache.DefaultTTL = "-5m"
	if got := c.TTL(); got.Hours() != 24 {
		t.Errorf("TTL() with a negative value = %v, want the 24h fallback", got)
	}
}

func TestAutoUpdateEnabled(t *testing.T) {
	if !AutoUpdateEnabled(nil) {
		t.Error("a nil config should default to auto-update enabled")
	}
	c := Default()
	c.AutoUpdate = false
	if AutoUpdateEnabled(c) {
		t.Error("auto_update=false should disable")
	}

	c.AutoUpdate = true
	t.Setenv("TEXT_NO_UPDATE", "1")
	if AutoUpdateEnabled(c) {
		t.Error("TEXT_NO_UPDATE=1 should disable")
	}
	// "0"/"false" mean "not set", not "disable" — an off-of-off must not
	// accidentally turn updates off.
	t.Setenv("TEXT_NO_UPDATE", "0")
	if !AutoUpdateEnabled(c) {
		t.Error("TEXT_NO_UPDATE=0 should not disable")
	}
	t.Setenv("TEXT_NO_UPDATE", "false")
	if !AutoUpdateEnabled(c) {
		t.Error("TEXT_NO_UPDATE=false should not disable")
	}
}

func TestGetSetRoundTripKeys(t *testing.T) {
	// Every key advertised by Keys() must be readable via Get, or `text config
	// list` would show a blank for a key that actually exists.
	c := Default()
	for _, k := range Keys() {
		if _, ok := c.Get(k); !ok {
			t.Errorf("Keys() advertises %q but Get does not know it", k)
		}
	}
	if _, ok := c.Get("nope.not.a.key"); ok {
		t.Error("Get accepted an unknown key")
	}
}

func TestExpandHome(t *testing.T) {
	if got := ExpandHome("/absolute/path"); got != "/absolute/path" {
		t.Errorf("ExpandHome mangled an absolute path: %q", got)
	}
	if got := ExpandHome(""); got != "" {
		t.Errorf("ExpandHome('') = %q", got)
	}
	if got := ExpandHome("~/secrets/key.json"); got == "~/secrets/key.json" {
		t.Error("ExpandHome did not expand a leading ~")
	}
}
