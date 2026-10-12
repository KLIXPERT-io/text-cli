package data

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

func codeOf(err error) errs.Code {
	if e, ok := err.(*errs.E); ok {
		return e.Code
	}
	return ""
}

func TestParseID(t *testing.T) {
	tests := []struct {
		name           string
		raw            string
		wantProvider   string
		wantCapability string
		wantErr        bool
	}{
		{name: "provider and a nested capability", raw: "fullenrich/people/lookup", wantProvider: "fullenrich", wantCapability: "people/lookup"},
		{name: "case and surrounding space are forgiven", raw: "  FullEnrich/People/Lookup ", wantProvider: "fullenrich", wantCapability: "people/lookup"},
		{name: "a hyphenated provider", raw: "finance-yahoo-com/equities/quote", wantProvider: "finance-yahoo-com", wantCapability: "equities/quote"},
		{name: "a provider alone is not an id", raw: "fullenrich", wantErr: true},
		{name: "an empty capability is not an id", raw: "fullenrich/", wantErr: true},
		{name: "a query string cannot ride along", raw: "fullenrich/people/lookup?x=1", wantErr: true},
		{name: "a path cannot climb out", raw: "../fullenrich/people", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider, capability, err := ParseID(tc.raw)
			if tc.wantErr {
				if codeOf(err) != errs.CodeInvalidArgs {
					t.Fatalf("err = %v, want invalid_args", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseID: %v", err)
			}
			if provider != tc.wantProvider || capability != tc.wantCapability {
				t.Errorf("got %q / %q, want %q / %q", provider, capability, tc.wantProvider, tc.wantCapability)
			}
		})
	}
}

func TestContactRefusal(t *testing.T) {
	tests := []struct {
		name       string
		capability string
		options    map[string]any
		refused    bool
	}{
		{name: "a profile lookup", capability: "people/lookup", refused: false},
		{name: "an enrichment without contact data", capability: "people/enrich-base-no-contact", refused: false},
		{name: "a word that only contains contact", capability: "companies/contacts-page", refused: false},
		{name: "the contacts capabilities", capability: "contacts/mobile", refused: true},
		{name: "an enrichment that adds contact data", capability: "people/enrich-premium-with-contact", refused: true},
		{name: "a reveal option switched on", capability: "people/match", options: map[string]any{"reveal_phone_number": "true"}, refused: true},
		{name: "a reveal option switched off", capability: "people/match", options: map[string]any{"reveal_phone_number": false}, refused: false},
		{name: "a reveal option set by number", capability: "people/match", options: map[string]any{"reveal_personal_emails": json.Number("1")}, refused: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContactRefusal(tc.capability, tc.options) != ""; got != tc.refused {
				t.Errorf("refused = %v, want %v", got, tc.refused)
			}
		})
	}
}

func TestBudgetCeiling(t *testing.T) {
	tests := []struct {
		name    string
		budget  Budget
		want    int
		wantErr bool
	}{
		{name: "the configured ceiling stands without a cap", budget: Budget{MaxCredits: 30}, want: 30},
		{name: "a raised ceiling stands without a cap", budget: Budget{MaxCredits: 110, Explicit: true}, want: 110},
		{name: "a negative ceiling means the default", budget: Budget{MaxCredits: -1}, want: DefaultMaxCredits},
		{name: "zero allows free tools only", budget: Budget{MaxCredits: 0}, want: 0},
		{name: "a cap quietly lowers the default nobody asked for", budget: Budget{MaxCredits: 30, Cap: 0, Capped: true}, want: 0},
		{name: "a flag inside the cap stands", budget: Budget{MaxCredits: 5, Explicit: true, Cap: 10, Capped: true}, want: 5},
		{name: "a flag past the cap is refused", budget: Budget{MaxCredits: 110, Explicit: true, Cap: 30, Capped: true}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.budget.Ceiling()
			if tc.wantErr {
				if codeOf(err) != errs.CodeQuotaExceeded {
					t.Fatalf("err = %v, want quota_exceeded", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("Ceiling() = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestCapFromEnv(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		set        bool
		wantCap    int
		wantCapped bool
		wantErr    bool
	}{
		{name: "unset is no cap", set: false},
		{name: "empty is no cap", value: " ", set: true},
		{name: "zero caps at free tools", value: "0", set: true, wantCap: 0, wantCapped: true},
		{name: "a number caps there", value: "25", set: true, wantCap: 25, wantCapped: true},
		{name: "a typo is an error, not no cap", value: "thirty", set: true, wantErr: true},
		{name: "a negative cap is an error", value: "-1", set: true, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(EnvCreditCap, tc.value)
			} else {
				t.Setenv(EnvCreditCap, "")
			}
			capCredits, capped, err := CapFromEnv()
			if tc.wantErr {
				if codeOf(err) != errs.CodeInvalidArgs {
					t.Fatalf("err = %v, want invalid_args", err)
				}
				return
			}
			if err != nil || capped != tc.wantCapped || capCredits != tc.wantCap {
				t.Errorf("CapFromEnv() = %d, %v, %v; want %d, %v", capCredits, capped, err, tc.wantCap, tc.wantCapped)
			}
		})
	}
}

func TestRecordsOf(t *testing.T) {
	tests := []struct {
		name string
		res  *Result
		want int
	}{
		{name: "the records member's elements", res: &Result{RecordsKey: "people", Data: json.RawMessage(`{"people":[{"a":1},{"b":2}],"metadata":{}}`)}, want: 2},
		{name: "the whole answer when no member is named", res: &Result{Data: json.RawMessage(`{"metrics":[1,2,3]}`)}, want: 1},
		{name: "the whole answer when the named member is not a list", res: &Result{RecordsKey: "people", Data: json.RawMessage(`{"people":{"a":1}}`)}, want: 1},
		{name: "nothing for no answer", res: &Result{}, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(RecordsOf(tc.res)); got != tc.want {
				t.Errorf("records = %d, want %d", got, tc.want)
			}
		})
	}
}

// fakeSource is a one-tool catalogue that records what it was asked.
type fakeSource struct {
	tool   Tool
	result Result
	looked int
	ran    []Call
}

func (f *fakeSource) Name() string { return "fake" }

func (f *fakeSource) FindTools(_ context.Context, opts FindOptions) ([]Tool, int, error) {
	f.looked++
	if opts.Provider != f.tool.Provider || opts.Capability != f.tool.Capability {
		return []Tool{}, 0, nil
	}
	return []Tool{f.tool}, 1, nil
}

func (f *fakeSource) RunTool(_ context.Context, call Call) (*Result, error) {
	f.ran = append(f.ran, call)
	res := f.result
	return &res, nil
}

// browseOnly is a catalogue that can be listed and cannot run anything.
type browseOnly struct{ inner *fakeSource }

func (b browseOnly) Name() string { return "browse-only" }

func (b browseOnly) FindTools(ctx context.Context, opts FindOptions) ([]Tool, int, error) {
	return b.inner.FindTools(ctx, opts)
}

func newFake() *fakeSource {
	one := 1
	return &fakeSource{
		tool: Tool{ID: "fullenrich/people/lookup", Provider: "fullenrich", Capability: "people/lookup",
			Name: "Person lookup", Credits: 5, PerRecord: true, RecordsKey: "people"},
		result: Result{Credits: 5, Records: &one, RequestID: "r1",
			Data: json.RawMessage(`{"people":[{"full_name":"Ada Example"}]}`)},
	}
}

func TestRunSettlesEverythingBeforeSpending(t *testing.T) {
	ctx := context.Background()
	call := Call{Provider: "fullenrich", Capability: "people/lookup", Options: map[string]any{"person_name": "Ada Example"}}

	tests := []struct {
		name       string
		setup      func(*fakeSource)
		call       Call
		budget     Budget
		wantCode   errs.Code
		wantLooked bool
		wantRan    bool
	}{
		{name: "a run within the ceiling runs", call: call, budget: Budget{MaxCredits: 30}, wantLooked: true, wantRan: true},
		{name: "contact data is refused before the catalogue is asked",
			call:     Call{Provider: "fullenrich", Capability: "contacts/mobile"},
			budget:   Budget{MaxCredits: 30},
			wantCode: errs.CodeInvalidArgs},
		{name: "a price above the ceiling is refused after the free lookup",
			setup:      func(f *fakeSource) { f.tool.Credits = 110; f.tool.PerRecord = false },
			call:       call,
			budget:     Budget{MaxCredits: 30},
			wantCode:   errs.CodeQuotaExceeded,
			wantLooked: true},
		{name: "an unknown tool is not found and costs nothing",
			call:       Call{Provider: "fullenrich", Capability: "people/teleport"},
			budget:     Budget{MaxCredits: 30},
			wantCode:   errs.CodeNotFound,
			wantLooked: true},
		{name: "a malformed request id is refused up front",
			call:     Call{Provider: "fullenrich", Capability: "people/lookup", RequestID: "has spaces"},
			budget:   Budget{MaxCredits: 30},
			wantCode: errs.CodeInvalidArgs},
		{name: "an empty answer is not found",
			setup: func(f *fakeSource) {
				zero := 0
				f.result.Records = &zero
				f.result.Data = json.RawMessage(`{"people":[]}`)
			},
			call:       call,
			budget:     Budget{MaxCredits: 30},
			wantCode:   errs.CodeNotFound,
			wantLooked: true,
			wantRan:    true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			if tc.setup != nil {
				tc.setup(f)
			}
			res, err := Run(ctx, f, tc.call, tc.budget)
			if tc.wantCode != "" {
				if codeOf(err) != tc.wantCode {
					t.Fatalf("err = %v, want %s", err, tc.wantCode)
				}
			} else if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if (f.looked > 0) != tc.wantLooked {
				t.Errorf("looked up = %d times, want looked = %v", f.looked, tc.wantLooked)
			}
			if (len(f.ran) > 0) != tc.wantRan {
				t.Errorf("ran = %d times, want ran = %v", len(f.ran), tc.wantRan)
			}
			if tc.wantCode == "" && tc.wantRan {
				if res.Tool != "fullenrich/people/lookup" || res.Name != "Person lookup" || res.RecordsKey != "people" {
					t.Errorf("result = %+v, want the catalogue's id, name and record key on it", res)
				}
			}
		})
	}
}

func TestRunNeedsASourceThatCanRun(t *testing.T) {
	_, err := Run(context.Background(), browseOnly{newFake()}, Call{Provider: "fullenrich", Capability: "people/lookup"}, Budget{MaxCredits: 30})
	if codeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("err = %v, want provider_unavailable", err)
	}
	if !strings.Contains(err.Error(), "cannot run tools") {
		t.Errorf("err = %v, want it to say the source cannot run tools", err)
	}
}

func TestRegistry(t *testing.T) {
	if Default() != SourceAlexandria {
		t.Errorf("Default() = %q, want %q", Default(), SourceAlexandria)
	}
	if _, err := Open("nope"); codeOf(err) != errs.CodeInvalidArgs {
		t.Errorf("Open(nope) err = %v, want invalid_args", err)
	}
	src, err := Open("")
	if err != nil || src.Name() != SourceAlexandria {
		t.Fatalf("Open(\"\") = %v, %v; want the default source", src, err)
	}
	if _, err := RequireRunner(src); err != nil {
		t.Errorf("the default source cannot run tools: %v", err)
	}
}
