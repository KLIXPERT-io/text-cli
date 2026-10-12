// Package data asks structured-data catalogues for records a web page only
// paraphrases: a company's filings, a person's employment history, an
// economic series, the traffic of a website.
//
// It is the record counterpart to `text fetch`. fetch turns a page into prose;
// this package finds the tool that holds a fact as data, says what it costs,
// and runs it:
//
//	text data find "a listed company's income statement"
//	text data tool fiscal-ai/financials/income-statement
//	text data run fiscal-ai/financials/income-statement --opt ticker=MSFT
//
// The types are source-neutral. Firecrawl's Alexandria catalogue is the first
// backend, but a caller parsing `text data` output must not have to change when
// a second catalogue is added, so anything only one source can produce is
// `omitempty`.
//
// Listing the catalogue is free, and it is the one thing every source must do.
// Running a tool costs credits and is a capability interface — the design of
// research.PaperInspector — so a catalogue that can only be browsed implements
// Source and nothing else.
package data

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

const (
	// DefaultTimeout bounds one call. A run is a third-party API behind
	// Firecrawl rather than a lookup in Firecrawl's own index, and some
	// providers take the better part of a minute.
	DefaultTimeout = 60 * time.Second
	// DefaultLimit is how many tools a listing returns when the caller says
	// nothing. Ten is a screenful and a reasonable prompt budget.
	DefaultLimit = 10
	// MaxLimit is the catalogue's own page cap; Alexandria refuses more.
	MaxLimit = 100
	// DefaultMaxCredits is the most one run may cost unless the caller raises
	// it. Thirty covers almost the whole catalogue — a company profile, an
	// income statement, a person lookup — and stops at the web-traffic and
	// contact-data tools that run from forty to five hundred and fifty credits.
	DefaultMaxCredits = 30
	// EnvCreditCap is a ceiling no flag can raise. A machine where an agent
	// drives the CLI — a sandbox with the account's key in its environment —
	// sets it so that --max-credits, which the agent can type, cannot spend
	// past it. Unset means no cap.
	EnvCreditCap = "TEXT_DATA_CREDIT_CAP"
)

// Tool is one entry of a catalogue: a provider capability that answers a
// structured question.
type Tool struct {
	// ID is "provider/capability", the form every command takes.
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Capability string `json:"capability"`
	// Name is the catalogue's short label, "Person lookup".
	Name        string `json:"name,omitempty"`
	Description string `json:"description"`
	// Credits is the listed price: per call, or per record returned when
	// PerRecord is set. A search billed per record costs Credits times what
	// it returns, bounded by its own page size.
	Credits   int  `json:"credits"`
	PerRecord bool `json:"per_record,omitempty"`
	// RequiresOneOf lists groups of option names; a run sets at least one
	// option from every group.
	RequiresOneOf [][]string `json:"requires_one_of,omitempty"`
	// Options are the inputs the tool takes, as the catalogue describes them:
	// name, type, what it is for, default and bounds. They stay maps because
	// each provider describes its options a little differently, and whoever
	// reads them is deciding how to fill them in.
	Options []map[string]any `json:"options,omitempty"`
	// Returns is the catalogue's one-line description of the answer, and
	// RecordsKey the member of the answer holding its records when it is a
	// list — "people", "companies". Both come with a lookup by id only.
	Returns    string `json:"returns,omitempty"`
	RecordsKey string `json:"records_key,omitempty"`
	// Refused says this CLI will not run the tool, and why. Refused tools are
	// listed anyway, so the answer comes from the listing rather than from a
	// failed run.
	Refused string `json:"refused,omitempty"`
}

// FindOptions narrows a catalogue listing. At least one of Query, Sites and
// Provider has to be set.
type FindOptions struct {
	// Query describes the data needed, in words. It matches by meaning, and
	// loosely: "a person's profile and career history" finds the people tools,
	// "employer and job history" finds job boards.
	Query string
	// Sites lists addresses whose site a provider covers — a LinkedIn profile,
	// an X post — and asks for those providers' tools.
	Sites []string
	// Provider limits the listing to one provider; with Capability as well it
	// names exactly one tool.
	Provider   string
	Capability string
	Limit      int
	// Detail adds each tool's options. A listing without them is a fraction of
	// the size, and a lookup by id always has them.
	Detail  bool
	Timeout time.Duration
}

// EffectiveLimit returns the listing size, defaulted and capped.
func (o FindOptions) EffectiveLimit() int {
	switch {
	case o.Limit <= 0:
		return DefaultLimit
	case o.Limit > MaxLimit:
		return MaxLimit
	default:
		return o.Limit
	}
}

// EffectiveTimeout returns the per-call timeout, defaulted.
func (o FindOptions) EffectiveTimeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

// Call is one run of a tool.
type Call struct {
	Provider   string
	Capability string
	// Options are the tool's inputs, sent as given.
	Options map[string]any
	// RequestID is the idempotency key. The catalogue answers a repeated key
	// with the first answer instead of running — and billing — the tool again,
	// so a caller retrying a failed run passes the key the first attempt used.
	// Empty means a fresh one.
	RequestID string
	Timeout   time.Duration
}

// EffectiveTimeout returns the per-call timeout, defaulted.
func (c Call) EffectiveTimeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// Result is what a tool answered.
type Result struct {
	Tool       string `json:"tool"`
	Provider   string `json:"provider"`
	Capability string `json:"capability"`
	Name       string `json:"name,omitempty"`
	// Options are the inputs the tool was run with — what makes the answer
	// reproducible, and so part of what gets cited.
	Options map[string]any `json:"options"`
	// Credits is what this run was charged; a cached answer reports what it
	// cost when it was bought.
	Credits int `json:"credits"`
	// Records is how many records came back, when the provider counts them.
	Records *int `json:"records,omitempty"`
	// RecordsKey names the member of Data holding the records, when the tool
	// returns a list.
	RecordsKey string `json:"records_key,omitempty"`
	RequestID  string `json:"request_id"`
	// Data is the provider's answer, verbatim and in the provider's key order.
	Data json.RawMessage `json:"data"`
}

// Source is a catalogue of data tools. Listing it is the one thing every
// source must do.
//
// A source is expected to be safe for reuse across calls within a process and
// to translate its own failures into *errs.E, because the caller renders the
// code, not the prose.
type Source interface {
	// Name is the stable identifier used by --source and echoed in output.
	Name() string
	// FindTools lists catalogue entries, best first, with how many matched.
	FindTools(ctx context.Context, opts FindOptions) ([]Tool, int, error)
}

// Runner is the optional capability of running a tool. Capability, not
// requirement: a catalogue can be worth browsing without being able to run
// anything, and commands ask via RequireRunner.
type Runner interface {
	RunTool(ctx context.Context, call Call) (*Result, error)
}

// RequireRunner narrows an opened source to the run capability.
func RequireRunner(s Source) (Runner, error) {
	if r, ok := s.(Runner); ok {
		return r, nil
	}
	name := "source"
	if s != nil {
		name = s.Name()
	}
	have := strings.Join(RunnerSources(), ", ")
	if have == "" {
		have = "(none registered)"
	}
	return nil, errs.Newf(errs.CodeProviderUnavailable, "data source %q cannot run tools", name).
		WithHint("Sources that can: " + have + ". Choose one with --source, or set it with `text config set data.source <name>`.")
}

// RunnerSources lists the registered sources that can run a tool. Every one is
// constructed to type-assert on it, which is only affordable because Register
// documents factories as cheap.
func RunnerSources() []string {
	names := Names()
	out := make([]string, 0, len(names))
	for _, name := range names {
		s, err := Open(name)
		if err != nil || s == nil {
			continue
		}
		if _, ok := s.(Runner); ok {
			out = append(out, name)
		}
	}
	return out
}

// APIConfigurer is implemented by sources backed by a hosted API. Not part of
// Source, for the reason it is not part of research.Source: a local catalogue
// has no key to set.
type APIConfigurer interface {
	SetAPIKey(key string)
	SetBaseURL(url string)
}

var (
	// providerPattern and capabilityPattern are the shape of catalogue ids.
	// Checking them before a request means a typo fails locally, and an id can
	// never smuggle a path or a query string into one.
	providerPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,199}$`)
	capabilityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9/._-]{0,199}$`)
	// requestIDPattern is what Firecrawl takes in x-request-id.
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// ParseID splits "provider/capability" — the form `text data find` prints and
// the other two commands take — into its parts, lowercased.
func ParseID(raw string) (provider, capability string, err error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	provider, capability, ok := strings.Cut(v, "/")
	if !ok || provider == "" || capability == "" {
		return "", "", errs.Newf(errs.CodeInvalidArgs, "tool id %q is not provider/capability", raw).
			WithHint(`Take the id from "text data find", e.g. fullenrich/people/lookup.`)
	}
	if err := ValidateID(provider, capability); err != nil {
		return "", "", err
	}
	return provider, capability, nil
}

// ValidateID checks a provider and a capability against the catalogue's id
// shape.
func ValidateID(provider, capability string) error {
	if !providerPattern.MatchString(provider) {
		return errs.Newf(errs.CodeInvalidArgs, "%q is not a provider id", provider).
			WithHint(`Provider ids are lowercase, e.g. fullenrich or fiscal-ai. List them with "text data find".`)
	}
	if !capabilityPattern.MatchString(capability) {
		return errs.Newf(errs.CodeInvalidArgs, "%q is not a capability id", capability).
			WithHint(`Capability ids are lowercase paths, e.g. people/lookup. List them with "text data find".`)
	}
	return nil
}

// ValidRequestID reports whether an idempotency key has the shape Firecrawl
// accepts: 1 to 128 letters, digits, '.', '_', ':' or '-'.
func ValidRequestID(id string) bool { return requestIDPattern.MatchString(id) }

// ContactRefusal says why a run would be refused as a contact-data lookup, or
// returns "" when it would not be.
//
// The CLI looks up records about people and companies; it does not find a
// person's email address or phone number. So the tools that exist to find one
// — a provider's contacts/ capabilities, the "with-contact" enrichments — and
// the options that switch it on elsewhere are refused before anything is spent.
func ContactRefusal(capability string, options map[string]any) string {
	capability = strings.ToLower(strings.TrimSpace(capability))
	for _, segment := range strings.Split(capability, "/") {
		if segment == "contacts" || segment == "contact" {
			return capability + " finds contact details"
		}
	}
	if strings.Contains(capability, "with-contact") {
		return capability + " adds contact details"
	}
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.HasPrefix(strings.ToLower(key), "reveal_") && truthy(options[key]) {
			return "option " + key + " asks for contact details"
		}
	}
	return ""
}

// truthy reports whether an option value switches something on.
func truthy(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && b
	case float64:
		return v != 0
	case json.Number:
		f, err := v.Float64()
		return err == nil && f != 0
	default:
		return v != nil
	}
}

// Budget is the spending rule for one run.
type Budget struct {
	// MaxCredits is the most the run may cost, as the flag or the config gave
	// it, and Explicit says it came from the flag.
	MaxCredits int
	Explicit   bool
	// Cap, when Capped, is a ceiling MaxCredits may not exceed — the
	// environment's, which nothing typed on the command line can raise.
	Cap    int
	Capped bool
}

// CapFromEnv reads EnvCreditCap. A value that is not a non-negative integer is
// an error rather than "no cap": an operator who set a cap and mistyped it
// meant to have one.
func CapFromEnv() (cap int, capped bool, err error) {
	raw, ok := os.LookupEnv(EnvCreditCap)
	if !ok || strings.TrimSpace(raw) == "" {
		return 0, false, nil
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(raw))
	if convErr != nil || n < 0 {
		return 0, false, errs.Newf(errs.CodeInvalidArgs, "%s=%q is not a credit count", EnvCreditCap, raw).
			WithHint("Set it to a whole number of credits, e.g. 0 to allow only free tools.")
	}
	return n, true, nil
}

// Ceiling is the most the run may cost, or an error when the flag asks for
// more than the environment allows.
//
// A cap below the configured default lowers it quietly: nobody asked for the
// default, so a sandbox capped at 0 still runs free tools without a flag. Only
// a flag asking past the cap is refused, because then somebody did ask.
func (b Budget) Ceiling() (int, error) {
	ceiling := b.MaxCredits
	if ceiling < 0 {
		ceiling = DefaultMaxCredits
	}
	if !b.Capped || ceiling <= b.Cap {
		return ceiling, nil
	}
	if b.Explicit {
		return 0, errs.Newf(errs.CodeQuotaExceeded,
			"this environment caps a data run at %d credits, and %d was asked for", b.Cap, ceiling).
			WithHint(EnvCreditCap + " is set where this runs, and no flag can raise it. Ask whoever set it.")
	}
	return b.Cap, nil
}

// Lookup returns the catalogue entry for one tool, with its options, price and
// record key. It is free, and it is what a run is priced from.
func Lookup(ctx context.Context, src Source, provider, capability string, timeout time.Duration) (Tool, error) {
	if err := ValidateID(provider, capability); err != nil {
		return Tool{}, err
	}
	tools, _, err := src.FindTools(ctx, FindOptions{
		Provider:   provider,
		Capability: capability,
		Limit:      1,
		Detail:     true,
		Timeout:    timeout,
	})
	if err != nil {
		return Tool{}, err
	}
	for _, t := range tools {
		if t.Provider == provider && t.Capability == capability {
			return t, nil
		}
	}
	return Tool{}, errs.Newf(errs.CodeNotFound, "no tool %s/%s in the catalogue", provider, capability).
		WithHint(`Find one with "text data find" and copy its id.`)
}

// Run prices a tool from the catalogue, refuses it above the budget's ceiling,
// runs it, and reports an empty answer as not_found.
//
// The order is the point. The contact-data refusal and the price are both
// settled before the call that costs money, and a run refused for either has
// spent nothing.
func Run(ctx context.Context, src Source, call Call, budget Budget) (*Result, error) {
	call.Provider = strings.ToLower(strings.TrimSpace(call.Provider))
	call.Capability = strings.ToLower(strings.TrimSpace(call.Capability))
	if err := ValidateID(call.Provider, call.Capability); err != nil {
		return nil, err
	}
	if reason := ContactRefusal(call.Capability, call.Options); reason != "" {
		return nil, errs.Newf(errs.CodeInvalidArgs, "text does not look up personal contact details: %s", reason).
			WithHint("Use the tool's profile or lookup counterpart instead; an email address or a phone number is not something this CLI fetches.")
	}
	if call.RequestID != "" && !ValidRequestID(call.RequestID) {
		return nil, errs.New(errs.CodeInvalidArgs, "a request id is 1 to 128 letters, digits, '.', '_', ':' or '-'")
	}
	runner, err := RequireRunner(src)
	if err != nil {
		return nil, err
	}
	ceiling, err := budget.Ceiling()
	if err != nil {
		return nil, err
	}

	tool, err := Lookup(ctx, src, call.Provider, call.Capability, call.Timeout)
	if err != nil {
		return nil, err
	}
	if tool.Refused != "" {
		return nil, errs.Newf(errs.CodeInvalidArgs, "text does not run %s: %s", tool.ID, tool.Refused)
	}
	if tool.Credits > ceiling {
		price := strconv.Itoa(tool.Credits) + " credits"
		if tool.PerRecord {
			price += " per record"
		}
		hint := "Nothing was spent. Rerun with --max-credits " + strconv.Itoa(tool.Credits) +
			" once whoever pays has agreed to that price."
		if budget.Capped && tool.Credits > budget.Cap {
			hint = "Nothing was spent. " + EnvCreditCap + " caps a run at " + strconv.Itoa(budget.Cap) +
				" credits here, and no flag can raise it."
		}
		return nil, errs.Newf(errs.CodeQuotaExceeded, "%s costs %s, above the %d-credit ceiling for a run", tool.ID, price, ceiling).
			WithHint(hint)
	}

	res, err := runner.RunTool(ctx, call)
	if err != nil {
		return nil, err
	}
	res.Tool = tool.ID
	res.Name = tool.Name
	res.RecordsKey = tool.RecordsKey
	if res.Options == nil {
		res.Options = map[string]any{}
	}
	if empty(res) {
		return nil, errs.Newf(errs.CodeNotFound, "%s found nothing for these options (%d credits charged)", tool.ID, res.Credits).
			WithHint("Check the spelling, or pass an identifier the tool takes instead of a name — a profile URL, a domain, a ticker.")
	}
	return res, nil
}

// RecordsOf returns the records of an answer: the elements of its records
// member when the tool names one and it is a list, else the answer itself as
// the one record.
func RecordsOf(res *Result) []json.RawMessage {
	if res == nil || len(res.Data) == 0 {
		return []json.RawMessage{}
	}
	if res.RecordsKey != "" {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(res.Data, &members); err == nil {
			var list []json.RawMessage
			if err := json.Unmarshal(members[res.RecordsKey], &list); err == nil && list != nil {
				return list
			}
		}
	}
	return []json.RawMessage{res.Data}
}

// empty reports an answer that carries nothing: a zero record count, a null or
// empty payload, or an empty records member — the {"people": []} a lookup
// answers when it found nobody.
func empty(res *Result) bool {
	if res.Records != nil && *res.Records == 0 {
		return true
	}
	trimmed := strings.TrimSpace(string(res.Data))
	switch trimmed {
	case "", "null", "{}", "[]":
		return true
	}
	if res.RecordsKey != "" {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(res.Data, &members); err == nil {
			if list, ok := members[res.RecordsKey]; ok && strings.TrimSpace(string(list)) == "[]" {
				return true
			}
		}
	}
	return false
}

var (
	mu        sync.RWMutex
	factories = map[string]func() (Source, error){}
)

// Register adds a source factory. Factories are called lazily by Open, so
// registering must stay cheap — no clients, no credentials, no network. It
// panics on a duplicate name, which can only be a programming error and only
// ever at init time.
func Register(name string, factory func() (Source, error)) {
	mu.Lock()
	defer mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		panic("data: Register with empty name")
	}
	if _, dup := factories[key]; dup {
		panic("data: duplicate source " + key)
	}
	factories[key] = factory
}

// Open constructs the named source.
func Open(name string) (Source, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = Default()
	}
	mu.RLock()
	factory, ok := factories[key]
	mu.RUnlock()
	if !ok {
		known := strings.Join(Names(), ", ")
		if known == "" {
			known = "(none registered)"
		}
		return nil, errs.Newf(errs.CodeInvalidArgs, "unknown data source: %q", name).
			WithHint("Known sources: " + known + ". Select one with --source, or set it with `text config set data.source <name>`.")
	}
	s, err := factory()
	if err != nil {
		if _, ok := err.(*errs.E); ok {
			return nil, err
		}
		return nil, errs.Newf(errs.CodeProviderUnavailable, "data source %q: %s", key, err.Error())
	}
	return s, nil
}

// Names returns every registered source name, sorted.
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

// Registered reports whether a source name is known.
func Registered(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := factories[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// Default is the source used when none is named: the only one registered, or
// Alexandria when a build has several.
func Default() string {
	names := Names()
	switch {
	case len(names) == 0:
		return ""
	case len(names) == 1:
		return names[0]
	}
	if Registered(SourceAlexandria) {
		return SourceAlexandria
	}
	return names[0]
}
