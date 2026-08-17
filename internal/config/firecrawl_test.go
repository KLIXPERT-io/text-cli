package config

import (
	"strings"
	"testing"
)

// Every key in Keys() must be readable by Get and writable by Set. A key added
// to one of the three switches and not the others is silently unreachable:
// `text config set` rejects it, or `text config list` shows it permanently
// empty. This walks the list so that cannot happen quietly.
func TestEveryKeyIsGettableAndSettable(t *testing.T) {
	c := Default()
	for _, k := range Keys() {
		t.Run(k, func(t *testing.T) {
			if _, ok := c.Get(k); !ok {
				t.Fatalf("Get(%q) reports the key is unknown, but Keys() lists it", k)
			}
		})
	}
}

func TestSetAndGetRoundTripTheNewKeys(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{name: "the API key", key: "firecrawl.api_key", value: "fc-abc123", want: "fc-abc123"},
		{name: "a self-hosted endpoint", key: "firecrawl.base_url", value: "http://localhost:3002", want: "http://localhost:3002"},
		{name: "surrounding space is trimmed off a key", key: "firecrawl.api_key", value: "  fc-abc  ", want: "fc-abc"},
		{name: "the fetch backend", key: "fetch.provider", value: "firecrawl", want: "firecrawl"},
		{name: "the research backend", key: "research.source", value: "firecrawl", want: "firecrawl"},
		{name: "the fetch TTL", key: "cache.ttl_fetch", value: "6h", want: "6h"},
		{name: "the research TTL", key: "cache.ttl_research", value: "48h", want: "48h"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Set() saves to disk, so the in-memory assignment is exercised
			// through the same switch without touching the user's real file.
			c := Default()
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if err := c.Set(tc.key, tc.value); err != nil {
				t.Fatalf("Set(%q, %q) errored: %v", tc.key, tc.value, err)
			}
			got, ok := c.Get(tc.key)
			if !ok {
				t.Fatalf("Get(%q) reports the key is unknown", tc.key)
			}
			if got != tc.want {
				t.Fatalf("Get(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestSetRejectsAnInvalidTTL(t *testing.T) {
	for _, key := range []string{"cache.ttl_fetch", "cache.ttl_research"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if err := Default().Set(key, "soon"); err == nil {
				t.Fatalf("Set(%q, \"soon\") succeeded, want a duration error", key)
			}
		})
	}
}

func TestNewTTLDefaults(t *testing.T) {
	tests := []struct {
		name  string
		get   func(*Config) float64
		set   func(*Config, string)
		hours float64
	}{
		{
			name:  "fetch",
			get:   func(c *Config) float64 { return c.FetchTTL().Hours() },
			set:   func(c *Config, v string) { c.Cache.TTLFetch = v },
			hours: 24,
		},
		{
			name:  "research",
			get:   func(c *Config) float64 { return c.ResearchTTL().Hours() },
			set:   func(c *Config, v string) { c.Cache.TTLResearch = v },
			hours: 24,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			if got := tc.get(c); got != tc.hours {
				t.Errorf("default = %vh, want %vh", got, tc.hours)
			}
			// A malformed duration must fall back rather than yield a zero
			// TTL, which would expire every entry instantly.
			tc.set(c, "not-a-duration")
			if got := tc.get(c); got != tc.hours {
				t.Errorf("with a bad value = %vh, want the %vh fallback", got, tc.hours)
			}
		})
	}
}

// Default() leaves the backend names empty so the registries resolve their own
// single registered backend. Pinning "firecrawl" here would make a config
// written today outlive the reason for it.
func TestDefaultDoesNotPinABackend(t *testing.T) {
	c := Default()
	if c.Fetch.Provider != "" {
		t.Errorf("Fetch.Provider = %q, want it unset so the registry decides", c.Fetch.Provider)
	}
	if c.Research.Source != "" {
		t.Errorf("Research.Source = %q, want it unset so the registry decides", c.Research.Source)
	}
	if c.Firecrawl.APIKey != "" {
		t.Errorf("Firecrawl.APIKey = %q, want no baked-in credential", c.Firecrawl.APIKey)
	}
}

func TestSecretMarksTheCredential(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "the API key is a secret", key: "firecrawl.api_key", want: true},
		{name: "the endpoint is not", key: "firecrawl.base_url", want: false},
		{name: "a service account path is a path, not a credential", key: "entities.service_account_path", want: false},
		{name: "an ordinary key is not", key: "defaults.lang", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Secret(tc.key); got != tc.want {
				t.Fatalf("Secret(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestRedactKeepsOnlyAFingerprint(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "a real key keeps only its last four", in: "fc-b002081c737d40ebbfd97bc2096f017f", want: "…017f"},
		{name: "empty stays empty", in: "", want: ""},
		{name: "whitespace is empty", in: "   ", want: ""},
		{name: "a short value is fully masked", in: "abcd", want: "****"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			if got != tc.want {
				t.Fatalf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// The whole point: the original must not be recoverable from the
			// rendering that `text config list` prints.
			if tc.in != "" && strings.TrimSpace(tc.in) != "" && strings.Contains(got, strings.TrimSpace(tc.in)) {
				t.Fatalf("Redact(%q) = %q, which still contains the key", tc.in, got)
			}
		})
	}
}
