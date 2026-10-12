package data

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/firecrawl"
)

// SourceAlexandria is the registered name of this backend.
const SourceAlexandria = "alexandria"

func init() {
	Register(SourceAlexandria, func() (Source, error) { return &alexandriaSource{}, nil })
}

// alexandriaSource is Firecrawl's Alexandria catalogue: a few hundred
// providers' tools behind one endpoint, each a provider and a capability that
// answers with JSON rather than a page.
//
// Both halves go through POST /v2/scrape with an `alexandria` body in place of
// a URL. Listing is itself a tool — Firecrawl's own find-tools, which costs
// nothing — and running is the call that is billed.
type alexandriaSource struct {
	apiKey  string
	baseURL string
}

func (s *alexandriaSource) Name() string { return SourceAlexandria }

var (
	_ Runner        = (*alexandriaSource)(nil)
	_ APIConfigurer = (*alexandriaSource)(nil)
)

func (s *alexandriaSource) SetAPIKey(key string)   { s.apiKey = key }
func (s *alexandriaSource) SetBaseURL(base string) { s.baseURL = base }

const (
	// scrapePath is where every Alexandria call goes.
	scrapePath = "/v2/scrape"
	// catalogueProvider and findCapability name the free listing tool. The
	// catalogue's other own tools — the terms flow among them — are never run
	// through here: accepting a provider's terms is a person's decision.
	catalogueProvider = "firecrawl"
	findCapability    = "find-tools"
)

// alexandriaCall is one call in an `alexandria` request body.
type alexandriaCall struct {
	Provider   string `json:"provider"`
	Capability string `json:"capability"`
	Options    any    `json:"options"`
}

// alexandriaRequest executes a tool instead of scraping a page. It carries no
// url and no scrape options: the API refuses both beside `alexandria`.
type alexandriaRequest struct {
	Alexandria alexandriaCall `json:"alexandria"`
}

// findToolsOptions is the options object of a find-tools call.
type findToolsOptions struct {
	Query        string   `json:"query,omitempty"`
	URLs         []string `json:"urls,omitempty"`
	Providers    []string `json:"providers,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Level        string   `json:"level"`
	Expand       []string `json:"expand"`
	Limit        int      `json:"limit"`
}

// alexandriaResponse is the reply to an `alexandria` request that was
// accepted. Each call in it can still have failed on its own, which is what
// callError carries; refusals of the whole request are HTTP errors and the
// shared client has already translated them.
type alexandriaResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Alexandria []callResult `json:"alexandria"`
	} `json:"data"`
}

// callResult is the outcome of one call.
type callResult struct {
	Provider    string          `json:"provider"`
	Capability  string          `json:"capability"`
	CreditsCost int             `json:"creditsCost"`
	Records     *int            `json:"records"`
	Data        json.RawMessage `json:"data"`
	Error       *callError      `json:"error"`
}

// callError is a call that failed inside an accepted request. Status, when
// set, is the provider's own HTTP status, not Firecrawl's.
type callError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

// findToolsData is the payload of a find-tools call.
type findToolsData struct {
	Items []toolRecord `json:"items"`
	Total int          `json:"total"`
}

// toolRecord is one catalogue entry on the wire.
type toolRecord struct {
	ID            string           `json:"id"`
	Provider      string           `json:"provider"`
	Capability    string           `json:"capability"`
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	CreditsCost   int              `json:"creditsCost"`
	PerRecord     bool             `json:"perRecord"`
	RequiresOneOf [][]string       `json:"requiresOneOf"`
	Options       []map[string]any `json:"options"`
	Response      *struct {
		About string `json:"about"`
		Key   string `json:"key"`
	} `json:"response"`
}

func (s *alexandriaSource) FindTools(ctx context.Context, opts FindOptions) ([]Tool, int, error) {
	query := strings.TrimSpace(opts.Query)
	provider := strings.ToLower(strings.TrimSpace(opts.Provider))
	capability := strings.ToLower(strings.TrimSpace(opts.Capability))
	sites := make([]string, 0, len(opts.Sites))
	for _, raw := range opts.Sites {
		u, err := firecrawl.ValidateURL(raw)
		if err != nil {
			return nil, 0, err
		}
		sites = append(sites, u)
	}

	if query == "" && provider == "" && len(sites) == 0 {
		return nil, 0, errs.New(errs.CodeInvalidArgs, "nothing to look for").
			WithHint(`Describe the data, e.g. text data find "a company's profile from its domain", or list a provider with --provider.`)
	}
	if provider != "" && !providerPattern.MatchString(provider) {
		return nil, 0, errs.Newf(errs.CodeInvalidArgs, "%q is not a provider id", opts.Provider)
	}
	if capability != "" {
		// The catalogue resolves a capability only inside its provider: a bare
		// "people/lookup" is a dozen tools, and the joined id in this field is
		// refused outright.
		if provider == "" {
			return nil, 0, errs.New(errs.CodeInvalidArgs, "a capability is looked up inside its provider").
				WithHint("Pass both, or use the joined form with `text data tool provider/capability`.")
		}
		if !capabilityPattern.MatchString(capability) {
			return nil, 0, errs.Newf(errs.CodeInvalidArgs, "%q is not a capability id", opts.Capability)
		}
	}
	if err := firecrawl.RequireKey(s.apiKey); err != nil {
		return nil, 0, err
	}

	options := findToolsOptions{
		Query: query,
		URLs:  sites,
		Level: "tools",
		// No contract parts for a plain listing: the options are what make an
		// entry large. A lookup by id takes the response's description too, for
		// the record key a run's NDJSON is split on.
		Expand: []string{},
		Limit:  opts.EffectiveLimit(),
	}
	if opts.Detail {
		options.Expand = []string{"options"}
	}
	if provider != "" {
		options.Providers = []string{provider}
	}
	if capability != "" {
		options.Capabilities = []string{capability}
		options.Expand = []string{"options", "response"}
	}

	result, err := s.call(ctx, alexandriaCall{Provider: catalogueProvider, Capability: findCapability, Options: options}, "", opts.EffectiveTimeout())
	if err != nil {
		// The catalogue answers a capability it does not have as an invalid
		// option, which for a lookup by id means only one thing.
		if capability != "" && isCode(err, errs.CodeInvalidArgs) {
			return []Tool{}, 0, nil
		}
		return nil, 0, err
	}
	var page findToolsData
	if err := json.Unmarshal(result.Data, &page); err != nil {
		return nil, 0, errs.Newf(errs.CodeGeneric, "decode the tool listing: %s", err.Error()).
			WithHint("This usually means the API changed shape. Re-run with --verbose and report it.")
	}

	// Never nil: an empty listing renders as [] rather than null.
	tools := make([]Tool, 0, len(page.Items))
	for _, item := range page.Items {
		if item.Provider == "" || item.Capability == "" {
			// A provider or a group entry, not something that can be run.
			continue
		}
		t := Tool{
			ID:            item.ID,
			Provider:      item.Provider,
			Capability:    item.Capability,
			Name:          collapse(item.Name),
			Description:   collapse(item.Description),
			Credits:       item.CreditsCost,
			PerRecord:     item.PerRecord,
			RequiresOneOf: item.RequiresOneOf,
			Options:       item.Options,
			Refused:       ContactRefusal(item.Capability, nil),
		}
		if t.ID == "" {
			t.ID = item.Provider + "/" + item.Capability
		}
		if item.Response != nil {
			t.Returns = collapse(item.Response.About)
			t.RecordsKey = strings.TrimSpace(item.Response.Key)
		}
		tools = append(tools, t)
	}
	return tools, page.Total, nil
}

func (s *alexandriaSource) RunTool(ctx context.Context, call Call) (*Result, error) {
	provider := strings.ToLower(strings.TrimSpace(call.Provider))
	capability := strings.ToLower(strings.TrimSpace(call.Capability))
	if err := ValidateID(provider, capability); err != nil {
		return nil, err
	}
	if provider == catalogueProvider {
		return nil, errs.New(errs.CodeInvalidArgs, "Firecrawl's own tools are not run through text data").
			WithHint(`List the catalogue with "text data find"; provider terms are accepted by a person in the Firecrawl dashboard.`)
	}
	if err := firecrawl.RequireKey(s.apiKey); err != nil {
		return nil, err
	}
	requestID := strings.TrimSpace(call.RequestID)
	if requestID == "" {
		requestID = newRequestID()
	}
	if !ValidRequestID(requestID) {
		return nil, errs.New(errs.CodeInvalidArgs, "a request id is 1 to 128 letters, digits, '.', '_', ':' or '-'")
	}
	options := call.Options
	if options == nil {
		// {} rather than null: a capability with no required input still takes
		// an options object.
		options = map[string]any{}
	}

	result, err := s.call(ctx, alexandriaCall{Provider: provider, Capability: capability, Options: options}, requestID, call.EffectiveTimeout())
	if err != nil {
		// A timeout or an upstream failure may have happened after the tool
		// ran and was billed. The key is the only way back to that answer
		// without paying again, and a failed run prints nothing else — so it
		// goes in the hint.
		if e, ok := err.(*errs.E); ok && e.Retriable {
			e.Hint = strings.TrimSpace(e.Hint + " Retry with --request-id " + requestID +
				": if the first attempt completed, Firecrawl answers from it instead of charging again.")
			return nil, e
		}
		return nil, err
	}
	return &Result{
		Tool:       provider + "/" + capability,
		Provider:   firstNonEmpty(result.Provider, provider),
		Capability: firstNonEmpty(result.Capability, capability),
		Options:    options,
		Credits:    result.CreditsCost,
		Records:    result.Records,
		RequestID:  requestID,
		Data:       result.Data,
	}, nil
}

// call sends one `alexandria` call and returns its outcome, turning a call
// that failed inside an accepted request into this CLI's error codes.
func (s *alexandriaSource) call(ctx context.Context, call alexandriaCall, requestID string, timeout time.Duration) (callResult, error) {
	c := firecrawl.New(s.apiKey, s.baseURL)
	c.SetTimeout(timeout)

	var resp alexandriaResponse
	if err := c.PostIdempotent(ctx, scrapePath, requestID, alexandriaRequest{Alexandria: call}, &resp); err != nil {
		return callResult{}, err
	}
	if len(resp.Data.Alexandria) == 0 {
		return callResult{}, errs.New(errs.CodeAPI5xx, "Firecrawl answered without a result").WithRetry(0)
	}
	result := resp.Data.Alexandria[0]
	if e := result.Error; e != nil {
		return callResult{}, callErr(call, e)
	}
	return result, nil
}

// callErr maps a failed call onto the codes an agent branches on. The status
// in it is the provider's, so a 404 there is the provider saying it has nobody
// by that name — an answer, not a missing tool.
func callErr(call alexandriaCall, e *callError) *errs.E {
	id := call.Provider + "/" + call.Capability
	msg := collapse(e.Message)
	if msg == "" {
		msg = e.Code
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	switch {
	case e.Code == "unknown_provider", e.Code == "unknown_capability":
		return errs.Newf(errs.CodeNotFound, "no tool %s in the catalogue: %s", id, msg).
			WithHint(`Find one with "text data find" and copy its id.`)
	case strings.HasPrefix(e.Code, "invalid"), e.Status == http.StatusBadRequest, e.Status == http.StatusUnprocessableEntity:
		return errs.Newf(errs.CodeInvalidArgs, "%s refused the options: %s", id, msg).
			WithHint("List the options it takes with `text data tool " + id + "`.")
	case e.Status == http.StatusNotFound:
		return errs.Newf(errs.CodeNotFound, "%s found nothing: %s", id, msg).
			WithHint("Check the spelling, or pass an identifier the tool takes instead of a name — a profile URL, a domain, a ticker.")
	case e.Status >= 500:
		return errs.Newf(errs.CodeAPI5xx, "%s failed upstream (%d): %s", id, e.Status, msg).WithRetry(0)
	default:
		return errs.Newf(errs.CodeProviderUnavailable, "%s failed: %s", id, msg)
	}
}

// newRequestID makes an idempotency key for a run whose caller brought none. It
// only has to be unique; a caller that wants a retry answered from the first
// attempt passes --request-id with the key that attempt printed.
func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("text-%d", time.Now().UnixNano())
	}
	return "text-" + hex.EncodeToString(b[:])
}

// isCode reports whether err is an *errs.E with the given code.
func isCode(err error, code errs.Code) bool {
	e, ok := err.(*errs.E)
	return ok && e.Code == code
}

// collapse folds whitespace runs into single spaces; catalogue descriptions
// arrive with the line breaks of whoever wrote them.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
