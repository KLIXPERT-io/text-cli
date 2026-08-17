package entity

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// DefaultTimeout bounds a single provider call when the caller does not set one.
const DefaultTimeout = 30 * time.Second

// Options is what a provider is asked for. It is the whole contract: anything
// a provider needs that is not in here belongs in its own config section, not
// in this struct — otherwise every new backend widens it for everyone.
type Options struct {
	// Language is a BCP-47/ISO code (e.g. "de", "en", "pt-BR"). Empty means
	// "you figure it out": the provider detects the language itself.
	Language string
	// ServiceAccountPath points at a credentials file, for providers that need
	// one. Empty falls back to the provider's own resolution chain.
	ServiceAccountPath string
	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration
}

// EffectiveTimeout returns the per-call timeout, defaulted.
func (o Options) EffectiveTimeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

// Provider is an entity-analysis backend.
//
// Adding one is a single file plus one Register call in its init: no command
// wiring, no switch statement, no change to the output shapes. A provider is
// expected to be safe for reuse across calls within a process and to translate
// its own failures into *errs.E, because the caller renders the code, not the
// prose.
type Provider interface {
	// Name is the stable identifier used by --provider and echoed in output.
	Name() string
	// AnalyzeEntities extracts entities from one document.
	AnalyzeEntities(ctx context.Context, text string, opts Options) (*Result, error)
}

var (
	mu        sync.RWMutex
	factories = map[string]func() (Provider, error){}
)

// Register adds a provider factory. Factories are called lazily by Open, so
// registering must stay cheap — no clients, no credentials, no network. It
// panics on a duplicate name, which can only be a programming error and only
// ever at init time.
func Register(name string, factory func() (Provider, error)) {
	mu.Lock()
	defer mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		panic("entity: Register with empty name")
	}
	if _, dup := factories[key]; dup {
		panic("entity: duplicate provider " + key)
	}
	factories[key] = factory
}

// Open constructs the named provider.
func Open(name string) (Provider, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	mu.RLock()
	factory, ok := factories[key]
	mu.RUnlock()
	if !ok {
		known := strings.Join(Names(), ", ")
		if known == "" {
			known = "(none registered)"
		}
		return nil, errs.Newf(errs.CodeInvalidArgs, "unknown entity provider: %q", name).
			WithHint("Known providers: " + known + ". Set one with --provider or `text config set entities.provider <name>`.")
	}
	p, err := factory()
	if err != nil {
		if _, ok := err.(*errs.E); ok {
			return nil, err
		}
		return nil, errs.Newf(errs.CodeProviderUnavailable, "entity provider %q: %s", key, err.Error())
	}
	return p, nil
}

// Names returns every registered provider name, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Registered reports whether a provider name is known.
func Registered(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := factories[strings.ToLower(strings.TrimSpace(name))]
	return ok
}
