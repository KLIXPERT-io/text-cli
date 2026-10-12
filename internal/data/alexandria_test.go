package data

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// sentCall is an `alexandria` request as the stub API received it.
type sentCall struct {
	URL        *string `json:"url"`
	Alexandria struct {
		Provider   string         `json:"provider"`
		Capability string         `json:"capability"`
		Options    map[string]any `json:"options"`
	} `json:"alexandria"`
}

// stub is a recorded request to the fake API.
type stub struct {
	auth      string
	requestID string
	path      string
	call      sentCall
}

func newTestAlexandria(t *testing.T, got *stub, handler http.HandlerFunc) *alexandriaSource {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			got.auth = r.Header.Get("Authorization")
			got.requestID = r.Header.Get("X-Request-Id")
			got.path = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &got.call); err != nil {
				t.Errorf("request body %s: %v", raw, err)
			}
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	s := &alexandriaSource{}
	s.SetAPIKey("fc-test-key")
	s.SetBaseURL(srv.URL)
	return s
}

// answer writes an accepted request whose one call produced result.
func answer(result map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    map[string]any{"alexandria": []any{result}, "creditsCost": result["creditsCost"]},
		})
	}
}

func listing(items ...map[string]any) map[string]any {
	return map[string]any{
		"provider": "firecrawl", "capability": "find-tools", "creditsCost": 0,
		"data": map[string]any{"level": "tools", "items": items, "total": len(items)},
	}
}

func TestFindToolsAsksTheCatalogue(t *testing.T) {
	var got stub
	src := newTestAlexandria(t, &got, answer(listing(
		map[string]any{"id": "fullenrich/people/lookup", "provider": "fullenrich", "capability": "people/lookup",
			"name": "Person lookup", "description": "Resolve a  person.\n", "creditsCost": 5, "perRecord": true,
			"requiresOneOf": [][]string{{"person_name", "person_professional_network_url"}}},
		map[string]any{"id": "fullenrich/contacts/mobile", "provider": "fullenrich", "capability": "contacts/mobile",
			"name": "Mobile number", "description": "A phone number.", "creditsCost": 175, "perRecord": true},
		// A provider entry has no capability and is not a tool.
		map[string]any{"id": "fullenrich", "name": "FullEnrich"},
	)))

	tools, total, err := src.FindTools(context.Background(), FindOptions{Query: "a person's profile and career history"})
	if err != nil {
		t.Fatalf("FindTools: %v", err)
	}

	if got.path != "/v2/scrape" || got.auth != "Bearer fc-test-key" {
		t.Errorf("request = %s with %q, want the scrape endpoint with the key", got.path, got.auth)
	}
	if got.call.URL != nil {
		t.Error("a url went out beside an alexandria call")
	}
	a := got.call.Alexandria
	if a.Provider != "firecrawl" || a.Capability != "find-tools" {
		t.Errorf("call = %s/%s, want firecrawl/find-tools", a.Provider, a.Capability)
	}
	if a.Options["query"] != "a person's profile and career history" || a.Options["level"] != "tools" || a.Options["limit"] != float64(DefaultLimit) {
		t.Errorf("options = %v", a.Options)
	}
	if expand, _ := a.Options["expand"].([]any); len(expand) != 0 {
		t.Errorf("expand = %v, want nothing for a plain listing", a.Options["expand"])
	}

	if total != 3 || len(tools) != 2 {
		t.Fatalf("got %d tools of %d, want the 2 runnable entries of 3", len(tools), total)
	}
	if tools[0].ID != "fullenrich/people/lookup" || tools[0].Credits != 5 || !tools[0].PerRecord || tools[0].Description != "Resolve a person." {
		t.Errorf("tool = %+v", tools[0])
	}
	if tools[0].Refused != "" || tools[1].Refused == "" {
		t.Errorf("refused = %q / %q, want only the contact tool marked", tools[0].Refused, tools[1].Refused)
	}
}

func TestLookupReadsOneToolInFull(t *testing.T) {
	var got stub
	src := newTestAlexandria(t, &got, answer(listing(map[string]any{
		"id": "fullenrich/people/lookup", "provider": "fullenrich", "capability": "people/lookup",
		"name": "Person lookup", "description": "Resolve a person.", "creditsCost": 5, "perRecord": true,
		"options":  []map[string]any{{"name": "person_name", "type": "string"}},
		"response": map[string]any{"about": "The best match in the people array.", "key": "people"},
	})))

	tool, err := Lookup(context.Background(), src, "fullenrich", "people/lookup", 0)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	a := got.call.Alexandria
	providers, _ := a.Options["providers"].([]any)
	capabilities, _ := a.Options["capabilities"].([]any)
	expand, _ := a.Options["expand"].([]any)
	if len(providers) != 1 || providers[0] != "fullenrich" || len(capabilities) != 1 || capabilities[0] != "people/lookup" {
		t.Errorf("selectors = %v / %v, want the provider and the capability apart", a.Options["providers"], a.Options["capabilities"])
	}
	if len(expand) != 2 {
		t.Errorf("expand = %v, want options and response", a.Options["expand"])
	}
	if tool.RecordsKey != "people" || tool.Returns == "" || len(tool.Options) != 1 {
		t.Errorf("tool = %+v, want the record key, the description of the answer and the options", tool)
	}
}

func TestLookupOfAnUnknownCapabilityIsNotFound(t *testing.T) {
	// What the live catalogue answers for a capability it does not have.
	src := newTestAlexandria(t, nil, answer(map[string]any{
		"provider": "firecrawl", "capability": "find-tools", "creditsCost": 0,
		"error": map[string]any{"code": "invalid_option", "message": "Unknown or unavailable capabilities.", "status": 400},
	}))
	if _, err := Lookup(context.Background(), src, "fullenrich", "people/teleport", 0); codeOf(err) != errs.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestRunToolSendsTheCallUnderItsKey(t *testing.T) {
	tests := []struct {
		name      string
		requestID string
	}{
		{name: "the caller's key", requestID: "session-1:call-2"},
		{name: "a fresh key when none is given", requestID: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got stub
			src := newTestAlexandria(t, &got, answer(map[string]any{
				"provider": "fullenrich", "capability": "people/lookup", "creditsCost": 5, "records": 1,
				"data": json.RawMessage(`{"people":[{"full_name":"Ada Example","id":"p1"}]}`),
			}))

			res, err := src.RunTool(context.Background(), Call{
				Provider: "fullenrich", Capability: "people/lookup",
				Options:   map[string]any{"person_name": "Ada Example"},
				RequestID: tc.requestID,
			})
			if err != nil {
				t.Fatalf("RunTool: %v", err)
			}
			if tc.requestID != "" && got.requestID != tc.requestID {
				t.Errorf("x-request-id = %q, want the caller's %q", got.requestID, tc.requestID)
			}
			if got.requestID == "" || res.RequestID != got.requestID || !ValidRequestID(res.RequestID) {
				t.Errorf("request id sent %q, reported %q: want one well-formed key, reported back", got.requestID, res.RequestID)
			}
			if got.call.Alexandria.Options["person_name"] != "Ada Example" {
				t.Errorf("options = %v", got.call.Alexandria.Options)
			}
			if res.Credits != 5 || res.Records == nil || *res.Records != 1 {
				t.Errorf("result = %+v", res)
			}
			// Verbatim: the provider's key order is how a record reads.
			if string(res.Data) != `{"people":[{"full_name":"Ada Example","id":"p1"}]}` {
				t.Errorf("data = %s, want the payload as sent", res.Data)
			}
		})
	}
}

func TestRunToolRefusesWithoutSending(t *testing.T) {
	sent := 0
	src := newTestAlexandria(t, nil, func(w http.ResponseWriter, r *http.Request) { sent++ })

	tests := []struct {
		name string
		key  string
		call Call
		want errs.Code
	}{
		{name: "firecrawl's own tools, the terms flow among them", key: "fc-test-key",
			call: Call{Provider: "firecrawl", Capability: "terms/accept"}, want: errs.CodeInvalidArgs},
		{name: "no key", key: "", call: Call{Provider: "fullenrich", Capability: "people/lookup"}, want: errs.CodeAuthMissing},
		{name: "a bad id", key: "fc-test-key", call: Call{Provider: "Full Enrich", Capability: "people/lookup"}, want: errs.CodeInvalidArgs},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src.SetAPIKey(tc.key)
			if _, err := src.RunTool(context.Background(), tc.call); codeOf(err) != tc.want {
				t.Fatalf("err = %v, want %s", err, tc.want)
			}
		})
	}
	if sent != 0 {
		t.Errorf("%d requests went out for calls refused locally", sent)
	}
}

func TestRunToolMapsCallErrors(t *testing.T) {
	tests := []struct {
		name      string
		callError map[string]any
		want      errs.Code
		retriable bool
	}{
		{name: "an unknown provider is not found", callError: map[string]any{"code": "unknown_provider", "message": "no such provider"}, want: errs.CodeNotFound},
		{name: "bad options are the caller's to fix", callError: map[string]any{"code": "invalid_option", "message": "person_name must be a string", "status": 400}, want: errs.CodeInvalidArgs},
		{name: "a provider 404 is nobody by that name", callError: map[string]any{"code": "upstream_not_found", "message": "no match", "status": 404}, want: errs.CodeNotFound},
		{name: "a provider outage is retriable", callError: map[string]any{"code": "upstream_error", "message": "boom", "status": 502}, want: errs.CodeAPI5xx, retriable: true},
		{name: "any other provider refusal", callError: map[string]any{"code": "upstream_denied", "message": "nope", "status": 403}, want: errs.CodeProviderUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := newTestAlexandria(t, nil, answer(map[string]any{
				"provider": "fullenrich", "capability": "people/lookup", "creditsCost": 0, "error": tc.callError,
			}))
			_, err := src.RunTool(context.Background(), Call{Provider: "fullenrich", Capability: "people/lookup"})
			e, ok := err.(*errs.E)
			if !ok || e.Code != tc.want || e.Retriable != tc.retriable {
				t.Fatalf("err = %#v, want %s (retriable %v)", err, tc.want, tc.retriable)
			}
		})
	}
}

func TestRunToolNamesItsKeyWhenARetryMightPayTwice(t *testing.T) {
	src := newTestAlexandria(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "code": "request_unresolved", "error": "outcome unknown"})
	})
	_, err := src.RunTool(context.Background(), Call{Provider: "fullenrich", Capability: "people/lookup", RequestID: "job-42"})
	e, ok := err.(*errs.E)
	if !ok || !e.Retriable {
		t.Fatalf("err = %v, want a retriable error", err)
	}
	// A failed run prints nothing else, so the hint is the only place the key
	// that gets the first attempt's answer back can be read from.
	if !strings.Contains(e.Hint, "--request-id job-42") {
		t.Errorf("hint = %q, want it to name the request id", e.Hint)
	}
}

func TestRunToolRelaysThePageWhereTermsAreAccepted(t *testing.T) {
	src := newTestAlexandria(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false, "error": "Provider terms must be accepted.", "code": "THIRD_PARTY_DATA_TERMS_REQUIRED",
			"requiresAction": map[string]any{"type": "accept_terms", "terms": "apollo", "version": "2026-09",
				"url": "https://www.firecrawl.dev/app/settings?tab=data-sources"},
		})
	})
	_, err := src.RunTool(context.Background(), Call{Provider: "apollo", Capability: "people/match"})
	e, ok := err.(*errs.E)
	if !ok || e.Code != errs.CodeAuthDenied {
		t.Fatalf("err = %v, want auth_denied", err)
	}
	if !strings.Contains(e.Message, "apollo") || !strings.Contains(e.Hint, "https://www.firecrawl.dev/app/settings?tab=data-sources") {
		t.Errorf("err = %+v, want the provider in the message and the dashboard page in the hint", e)
	}
}
