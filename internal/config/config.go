// Package config loads ~/.config/text-cli/config.toml and merges it with the
// flag/env/default layers.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// AppDir is the directory name under the user config dir. It differs from the
// binary name ("text") on purpose — "text" is too generic for a shared
// ~/.config namespace.
const AppDir = "text-cli"

type Defaults struct {
	Output string `toml:"output"`
	// Lang is the analysis language: "auto", "en", "de", ...
	Lang string `toml:"lang"`
	// Metrics is the default --metrics selection ("auto" resolves by language).
	Metrics string `toml:"metrics"`
}

type Entities struct {
	// Provider names the entity backend, currently "google".
	Provider string `toml:"provider"`
	// ServiceAccountPath points at a Google service account key with access to
	// the Cloud Natural Language API.
	ServiceAccountPath string `toml:"service_account_path"`
	// Language forces the document language sent to the provider; empty lets
	// the provider detect it.
	Language string `toml:"language"`
}

type Cache struct {
	Dir         string `toml:"dir"`
	DefaultTTL  string `toml:"default_ttl"`
	TTLEntities string `toml:"ttl_entities"`
}

type Logging struct {
	Verbose bool   `toml:"verbose"`
	Format  string `toml:"format"`
}

type Config struct {
	Defaults   Defaults `toml:"defaults"`
	Entities   Entities `toml:"entities"`
	Cache      Cache    `toml:"cache"`
	Logging    Logging  `toml:"logging"`
	AutoUpdate bool     `toml:"auto_update"`
	// Path the config was loaded from (empty if defaults).
	path string
}

// Default returns built-in defaults.
func Default() *Config {
	return &Config{
		Defaults:   Defaults{Output: "", Lang: "auto", Metrics: "auto"},
		Entities:   Entities{Provider: "google"},
		Cache:      Cache{DefaultTTL: "24h", TTLEntities: "24h"},
		Logging:    Logging{Format: "text"},
		AutoUpdate: true,
	}
}

// AutoUpdateEnabled is the single source of truth for whether the background
// auto-updater (and `text update` apply paths) may run. It returns false when
// TEXT_NO_UPDATE is set to a non-empty value other than "0"/"false"
// (case-insensitive), or when c.AutoUpdate is false. A nil *Config is treated
// as defaults (AutoUpdate=true).
func AutoUpdateEnabled(c *Config) bool {
	if v, ok := os.LookupEnv("TEXT_NO_UPDATE"); ok && v != "" {
		switch strings.ToLower(v) {
		case "0", "false":
			// explicit off-of-off: treat as not set (do not disable)
		default:
			return false
		}
	}
	if c != nil && !c.AutoUpdate {
		return false
	}
	return true
}

// DataDir returns the base directory for all persistent data (cache, update
// state). Uses os.UserConfigDir(), i.e. ~/.config/text-cli on Linux,
// ~/Library/Application Support/text-cli on macOS.
func DataDir() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, AppDir), nil
}

// Path returns the location of config.toml.
func Path() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.toml"), nil
}

// Load reads the config file; a missing file yields defaults.
func Load() (*Config, error) {
	c := Default()
	p, err := Path()
	if err != nil {
		return c, err
	}
	c.path = p
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return c, err
	}
	if _, err := toml.Decode(string(b), c); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	c.path = p
	return c, nil
}

// Save writes config back to disk (mkdir -p).
func (c *Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

func (c *Config) LoadedPath() string { return c.path }

// TTL returns the cache default TTL parsed as a duration (24h fallback).
func (c *Config) TTL() time.Duration { return parseTTL(c.Cache.DefaultTTL, 24*time.Hour) }

// EntitiesTTL returns the cache TTL for entity analysis. Entity calls are
// billed per request, so the default is long: the same text yields the same
// entities.
func (c *Config) EntitiesTTL() time.Duration { return parseTTL(c.Cache.TTLEntities, 24*time.Hour) }

func parseTTL(v string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// Get returns the value at a dotted key (e.g. "defaults.lang").
func (c *Config) Get(key string) (string, bool) {
	switch key {
	case "defaults.output":
		return c.Defaults.Output, true
	case "defaults.lang":
		return c.Defaults.Lang, true
	case "defaults.metrics":
		return c.Defaults.Metrics, true
	case "entities.provider":
		return c.Entities.Provider, true
	case "entities.service_account_path":
		return c.Entities.ServiceAccountPath, true
	case "entities.language":
		return c.Entities.Language, true
	case "cache.dir":
		return c.Cache.Dir, true
	case "cache.default_ttl":
		return c.Cache.DefaultTTL, true
	case "cache.ttl_entities":
		return c.Cache.TTLEntities, true
	case "logging.verbose":
		return fmt.Sprintf("%v", c.Logging.Verbose), true
	case "logging.format":
		return c.Logging.Format, true
	case "auto_update":
		return fmt.Sprintf("%v", c.AutoUpdate), true
	}
	return "", false
}

// Set updates a dotted key and saves.
func (c *Config) Set(key, value string) error {
	switch key {
	case "defaults.output":
		if value != "" && !validOutput(value) {
			return fmt.Errorf("defaults.output must be json, ndjson, csv, table, or text")
		}
		c.Defaults.Output = value
	case "defaults.lang":
		c.Defaults.Lang = value
	case "defaults.metrics":
		c.Defaults.Metrics = value
	case "entities.provider":
		c.Entities.Provider = value
	case "entities.service_account_path":
		c.Entities.ServiceAccountPath = value
	case "entities.language":
		c.Entities.Language = value
	case "cache.dir":
		c.Cache.Dir = value
	case "cache.default_ttl":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid duration: %q", value)
		}
		c.Cache.DefaultTTL = value
	case "cache.ttl_entities":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid duration: %q", value)
		}
		c.Cache.TTLEntities = value
	case "logging.verbose":
		c.Logging.Verbose = value == "true" || value == "1"
	case "logging.format":
		if value != "text" && value != "json" {
			return fmt.Errorf("logging.format must be text or json")
		}
		c.Logging.Format = value
	case "auto_update":
		c.AutoUpdate = value == "true" || value == "1"
	default:
		return fmt.Errorf("unknown key: %s", key)
	}
	return c.Save()
}

func validOutput(v string) bool {
	switch v {
	case "json", "ndjson", "csv", "table", "text":
		return true
	}
	return false
}

// Keys returns the list of known keys (stable order).
func Keys() []string {
	return []string{
		"defaults.output",
		"defaults.lang",
		"defaults.metrics",
		"entities.provider",
		"entities.service_account_path",
		"entities.language",
		"cache.dir",
		"cache.default_ttl",
		"cache.ttl_entities",
		"logging.verbose",
		"logging.format",
		"auto_update",
	}
}

// ExpandHome expands a leading ~ in paths.
func ExpandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}
