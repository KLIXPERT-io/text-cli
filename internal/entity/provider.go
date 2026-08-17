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

// Provider is a text-analysis backend, and AnalyzeEntities is the one thing
// every backend must be able to do.
//
// Adding one is a single file plus one Register call in its init: no command
// wiring, no switch statement, no change to the output shapes. A provider is
// expected to be safe for reuse across calls within a process and to translate
// its own failures into *errs.E, because the caller renders the code, not the
// prose.
//
// Anything a backend may or may not be able to do lives in a capability
// interface next to this one — SentimentAnalyzer, TextClassifier — which a
// provider implements only if it really can.
type Provider interface {
	// Name is the stable identifier used by --provider and echoed in output.
	Name() string
	// AnalyzeEntities extracts entities from one document.
	AnalyzeEntities(ctx context.Context, text string, opts Options) (*Result, error)
}

// SentimentAnalyzer is the optional capability of scoring how a document feels.
// See sentiment.go for the result type and the label rule.
//
// It is deliberately NOT part of Provider. A knowledge-base backend — the
// Wikipedia one being built now — can name entities perfectly well without
// having any notion of polarity, and folding this method into Provider would
// force every future backend to stub out something it cannot honour. Commands
// ask for the capability instead (RequireSentiment), so the registry stays the
// single extension point and a thin provider simply does not implement it.
type SentimentAnalyzer interface {
	AnalyzeSentiment(ctx context.Context, text string, opts Options) (*SentimentResult, error)
}

// TextClassifier is the optional capability of sorting a document into content
// categories. Same reasoning as SentimentAnalyzer: capability, not requirement.
type TextClassifier interface {
	ClassifyText(ctx context.Context, text string, opts Options) (*ClassificationResult, error)
}

// RequireSentiment narrows an opened provider to the sentiment capability.
func RequireSentiment(p Provider) (SentimentAnalyzer, error) {
	if a, ok := p.(SentimentAnalyzer); ok {
		return a, nil
	}
	return nil, capabilityError(p, "sentiment analysis", SentimentProviders())
}

// RequireClassifier narrows an opened provider to the classification capability.
func RequireClassifier(p Provider) (TextClassifier, error) {
	if c, ok := p.(TextClassifier); ok {
		return c, nil
	}
	return nil, capabilityError(p, "text classification", ClassifierProviders())
}

// SentimentProviders lists the registered providers that can analyse sentiment.
func SentimentProviders() []string {
	return capableNames(func(p Provider) bool { _, ok := p.(SentimentAnalyzer); return ok })
}

// ClassifierProviders lists the registered providers that can classify text.
func ClassifierProviders() []string {
	return capableNames(func(p Provider) bool { _, ok := p.(TextClassifier); return ok })
}

// capableNames constructs every registered provider and keeps the ones matching
// the predicate. That is only affordable because Register documents factories as
// cheap — no client, no credentials, no network — and it is why the hint can
// name the backends that would have worked instead of saying "some other one".
//
// The constructed providers are deliberately not closed: a factory is allowed
// to hand out a shared instance, and closing it here would tear down a
// connection its real caller is still using. A freshly built provider holds no
// connection anyway.
func capableNames(supports func(Provider) bool) []string {
	names := Names()
	out := make([]string, 0, len(names))
	for _, name := range names {
		p, err := Open(name)
		if err != nil || p == nil {
			continue
		}
		if supports(p) {
			out = append(out, name)
		}
	}
	return out
}

// capabilityError reports a provider that exists but cannot answer this command.
//
// The code is CodeProviderUnavailable rather than CodeInvalidArgs on purpose:
// the invocation may carry no --provider at all (the name can come from
// entities.provider in the config), so blaming the arguments would point the
// user at a flag they never typed. An unknown provider name stays
// CodeInvalidArgs — that one really is a bad argument. The distinction is what
// lets an agent tell "you typo'd the backend" from "this backend cannot do
// sentiment, try another".
func capabilityError(p Provider, capability string, supported []string) error {
	name := "provider"
	if p != nil {
		name = p.Name()
	}
	have := strings.Join(supported, ", ")
	if have == "" {
		have = "(none registered)"
	}
	return errs.Newf(errs.CodeProviderUnavailable, "provider %q does not support %s", name, capability).
		WithHint("Providers that do: " + have + ". Choose one with --provider, or set it with `text config set entities.provider <name>`.")
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
