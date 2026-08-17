package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/cache"
	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/gdocs"
	"github.com/spf13/cobra"
)

func TestDocsIDResolution(t *testing.T) {
	const id = "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"

	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "a bare id", args: []string{id}, want: id},
		{name: "a URL", args: []string{"https://docs.google.com/document/d/" + id + "/edit"}, want: id},
		{name: "nothing at all", args: nil, wantErr: true},
		// Each docs command works on one document; several at once is what
		// --url is for.
		{name: "a second document", args: []string{id, id}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := docsID(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("docsID(%v) = %q, want an error", tc.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("docsID(%v) errored: %v", tc.args, err)
			}
			if got != tc.want {
				t.Fatalf("docsID(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestDocsBody(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		flagText string
		want     string
		wantErr  bool
	}{
		{name: "the arguments are the text", args: []string{"one", "two"}, want: "one two"},
		{name: "--text wins over the arguments", args: []string{"ignored"}, flagText: "used", want: "used"},
		{name: "nothing given", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &State{}
			got, err := s.docsBody(tc.args, tc.flagText)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("docsBody = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("docsBody errored: %v", err)
			}
			if got != tc.want {
				t.Fatalf("docsBody = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDocsBodyIsNotStripped(t *testing.T) {
	// State.LoadInput reduces markup to prose, which is right for every command
	// that measures a document and wrong for every command that writes one: a
	// heading typed as "## Rollout" must reach the document as typed.
	s := &State{}
	got, err := s.docsBody(nil, "## Rollout\n\n- one\n- two")
	if err != nil {
		t.Fatalf("docsBody errored: %v", err)
	}
	if !strings.Contains(got, "## Rollout") || !strings.Contains(got, "- one") {
		t.Fatalf("docsBody = %q, want the markdown left alone", got)
	}
}

func TestServiceAccountPathResolutionOrder(t *testing.T) {
	cfg := &config.Config{
		Docs:     config.Docs{ServiceAccountPath: "/from/docs-config.json"},
		Entities: config.Entities{ServiceAccountPath: "/from/entities-config.json"},
	}

	tests := []struct {
		name     string
		explicit string
		env      map[string]string
		cfg      *config.Config
		want     string
	}{
		{name: "the flag wins", explicit: "/from/flag.json", env: map[string]string{"TEXT_SERVICE_ACCOUNT": "/from/env.json"}, cfg: cfg, want: "/from/flag.json"},
		// Exporting TEXT_SERVICE_ACCOUNT in CI must override a developer's
		// stored config without editing the file.
		{name: "then the environment", env: map[string]string{"TEXT_SERVICE_ACCOUNT": "/from/env.json"}, cfg: cfg, want: "/from/env.json"},
		{name: "then the Google standard variable", env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/from/gac.json"}, cfg: cfg, want: "/from/gac.json"},
		{name: "then the docs config", cfg: cfg, want: "/from/docs-config.json"},
		{
			// One machine usually has one Google credential; a user who already
			// configured entity extraction should not have to configure a
			// second thing to read a document.
			name: "then the entities config",
			cfg:  &config.Config{Entities: config.Entities{ServiceAccountPath: "/from/entities-config.json"}},
			want: "/from/entities-config.json",
		},
		{name: "nothing configured means default credentials", cfg: config.Default(), want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEXT_SERVICE_ACCOUNT", tc.env["TEXT_SERVICE_ACCOUNT"])
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", tc.env["GOOGLE_APPLICATION_CREDENTIALS"])
			s := &State{Cfg: tc.cfg}
			if got := s.serviceAccountPath(tc.explicit); got != tc.want {
				t.Fatalf("serviceAccountPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOneLineFlattensForTheFlatFormats(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "a short string is untouched", in: "kurz", max: 10, want: "kurz"},
		{name: "line breaks become spaces", in: "eins\nzwei", max: 20, want: "eins zwei"},
		{name: "runs of whitespace collapse", in: "eins    zwei", max: 20, want: "eins zwei"},
		{name: "a long string is cut with an ellipsis", in: "abcdefghij", max: 4, want: "abcd…"},
		// Cutting by bytes would split "ä" and produce invalid UTF-8 in a table.
		{name: "the cut lands on a rune boundary", in: "äöüäöüäöü", max: 3, want: "äöü…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := oneLine(tc.in, tc.max); got != tc.want {
				t.Fatalf("oneLine(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

// --- command-level tests, against a fake Docs API -------------------------

// testDocID is a well-formed Drive id. The commands parse it before any call,
// so a short placeholder would fail at argument validation rather than
// exercising the command.
const testDocID = "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"

// docsTestServer answers the two calls the docs commands make.
type docsTestServer struct {
	t     *testing.T
	batch map[string]any
	calls int
}

func (d *docsTestServer) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.calls++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, ":batchUpdate"):
			_ = json.NewDecoder(r.Body).Decode(&d.batch)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"documentId":   "doc-1",
				"writeControl": map[string]any{"requiredRevisionId": "rev-2"},
				"replies":      []any{map[string]any{"replaceAllText": map[string]any{"occurrencesChanged": 1}}},
			})
		case strings.HasSuffix(r.URL.Path, "/comments"):
			_ = json.NewEncoder(w).Encode(map[string]any{"comments": []any{
				map[string]any{
					"id":                "c1",
					"content":           "Substantivstil — bitte umformulieren.",
					"author":            map[string]any{"displayName": "Ada"},
					"quotedFileContent": map[string]any{"value": "Die Inanspruchnahme"},
				},
			}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"documentId": "doc-1",
				"title":      "Leitfaden",
				"revisionId": "rev-1",
				"tabs": []any{map[string]any{
					"tabProperties": map[string]any{"tabId": "t.0", "title": "Entwurf"},
					"documentTab": map[string]any{"body": map[string]any{"content": []any{
						paragraphJSON("HEADING_1", "Leitfaden\n"),
						paragraphJSON("NORMAL_TEXT", "Die Inanspruchnahme der Leistung erfolgt auf Antrag.\n"),
					}}},
				}},
			})
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func paragraphJSON(style, content string) map[string]any {
	return map[string]any{"paragraph": map[string]any{
		"paragraphStyle": map[string]any{"namedStyleType": style},
		"elements":       []any{map[string]any{"textRun": map[string]any{"content": content}}},
	}}
}

// newDocsTestRoot builds a root carrying the docs command and points the client
// at a fake API, so the tests cover the command surface without a credential.
func newDocsTestRoot(t *testing.T, st *State, endpoint string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	if st.Cfg == nil {
		st.Cfg = config.Default()
	}
	if st.Cache == nil {
		st.Cache = cache.New(t.TempDir(), time.Hour)
	}
	if st.OutputFormat == "" {
		st.OutputFormat = "json"
	}
	t.Setenv("TEXT_SERVICE_ACCOUNT", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

	key := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(key, []byte(`{"type":"service_account","client_email":"text-cli@p.iam.gserviceaccount.com","project_id":"p"}`), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	st.Cfg.Docs.ServiceAccountPath = key

	original := openDocsFn
	openDocsFn = func(ctx context.Context, opts gdocs.Options) (*gdocs.Client, error) {
		opts.Endpoint = endpoint
		return original(ctx, opts)
	}
	t.Cleanup(func() { openDocsFn = original })

	root := &cobra.Command{Use: "text", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&st.OutputFormat, "output", st.OutputFormat, "")
	root.AddCommand(newDocsCmd())

	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetContext(context.WithValue(context.Background(), stateKey, st))
	return root, buf
}

func runDocs(t *testing.T, st *State, endpoint string, args ...string) (map[string]any, string, error) {
	t.Helper()
	root, buf := newDocsTestRoot(t, st, endpoint)
	root.SetArgs(append([]string{"docs"}, args...))
	err := root.Execute()
	out := buf.String()
	var env map[string]any
	if err == nil && st.OutputFormat == "json" {
		if jsonErr := json.Unmarshal([]byte(out), &env); jsonErr != nil {
			t.Fatalf("decode output %q: %v", out, jsonErr)
		}
	}
	return env, out, err
}

func TestDocsReadEmitsTheEnvelope(t *testing.T) {
	srv := &docsTestServer{t: t}
	env, _, err := runDocs(t, &State{}, srv.start(t), "read", testDocID)
	if err != nil {
		t.Fatalf("docs read: %v", err)
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("envelope = %v, want a data object", env)
	}
	if data["title"] != "Leitfaden" || data["revision_id"] != "rev-1" {
		t.Fatalf("data = %v", data)
	}
	if got := data["content"].(string); !strings.HasPrefix(got, "# Leitfaden") {
		t.Fatalf("content = %q, want markdown", got)
	}
	meta := env["meta"].(map[string]any)
	if meta["provider"] != docsProvider {
		t.Errorf("meta.provider = %v, want %q", meta["provider"], docsProvider)
	}
	// Nothing here is cached: a document may be being typed into right now.
	if meta["cached"] != false {
		t.Errorf("meta.cached = %v, want false", meta["cached"])
	}
}

func TestDocsReadTextOutputIsPipeSafe(t *testing.T) {
	srv := &docsTestServer{t: t}
	// --output text must print the document and nothing else, which is what
	// makes `text docs read <id> --output text | text lint` safe.
	_, out, err := runDocs(t, &State{OutputFormat: "text"}, srv.start(t), "read", testDocID)
	if err != nil {
		t.Fatalf("docs read: %v", err)
	}
	if !strings.HasPrefix(out, "# Leitfaden") {
		t.Fatalf("output = %q, want the document with no header line", out)
	}
	if strings.Contains(out, "revision") {
		t.Fatalf("output = %q, want no metadata in the pipe form", out)
	}
}

func TestDocsCommentsCarryTheQuotedPassage(t *testing.T) {
	srv := &docsTestServer{t: t}
	env, _, err := runDocs(t, &State{}, srv.start(t), "comments", testDocID)
	if err != nil {
		t.Fatalf("docs comments: %v", err)
	}
	comments := env["data"].(map[string]any)["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("comments = %v", comments)
	}
	// The quoted passage is what `docs replace --find` is given, so it must
	// survive to the output verbatim.
	if got := comments[0].(map[string]any)["quoted"]; got != "Die Inanspruchnahme" {
		t.Fatalf("quoted = %v", got)
	}
}

func TestDocsReplaceRefusesAnAmbiguousMatchBeforeWriting(t *testing.T) {
	srv := &docsTestServer{t: t}
	_, _, err := runDocs(t, &State{}, srv.start(t), "replace", testDocID,
		"--find", "e", "--replace", "x")
	if err == nil {
		t.Fatal("docs replace succeeded on an ambiguous match")
	}
	var e *errs.E
	if !asErr(err, &e) || e.Code != errs.CodeInvalidArgs {
		t.Fatalf("error = %v, want invalid_args", err)
	}
	if srv.batch != nil {
		t.Fatal("the document was written despite the guard")
	}
}

func TestDocsReplaceRequiresTheReplacementToBeStated(t *testing.T) {
	srv := &docsTestServer{t: t}
	// Omitting --replace and deleting the text are different intentions;
	// defaulting to "" would silently make the first one the second.
	_, _, err := runDocs(t, &State{}, srv.start(t), "replace", testDocID, "--find", "Die Inanspruchnahme")
	if err == nil {
		t.Fatal("docs replace ran without --replace")
	}
	if srv.calls != 0 {
		t.Fatal("the API was called before the arguments were checked")
	}
}

func TestDocsReplaceWritesWithTheRevisionPinned(t *testing.T) {
	srv := &docsTestServer{t: t}
	env, _, err := runDocs(t, &State{}, srv.start(t), "replace", testDocID,
		"--find", "Die Inanspruchnahme", "--replace", "Wer die Leistung nutzt")
	if err != nil {
		t.Fatalf("docs replace: %v", err)
	}
	if srv.batch == nil {
		t.Fatal("nothing was written")
	}
	control := srv.batch["writeControl"].(map[string]any)
	if control["requiredRevisionId"] != "rev-1" {
		t.Fatalf("writeControl = %v, want the revision the read returned", control)
	}
	data := env["data"].(map[string]any)
	if data["applied"] != true || data["occurrences"].(float64) != 1 {
		t.Fatalf("data = %v", data)
	}
}

func TestDocsDryRunDoesNotWrite(t *testing.T) {
	srv := &docsTestServer{t: t}
	env, _, err := runDocs(t, &State{}, srv.start(t), "replace", testDocID,
		"--find", "Die Inanspruchnahme", "--replace", "x", "--dry-run")
	if err != nil {
		t.Fatalf("docs replace --dry-run: %v", err)
	}
	if srv.batch != nil {
		t.Fatal("a dry run wrote to the document")
	}
	if env["data"].(map[string]any)["applied"] != false {
		t.Fatal("a dry run reported itself as applied")
	}
}

func TestDocsInsertAppendsAsANewParagraph(t *testing.T) {
	srv := &docsTestServer{t: t}
	if _, _, err := runDocs(t, &State{}, srv.start(t), "insert", testDocID, "Nachtrag."); err != nil {
		t.Fatalf("docs insert: %v", err)
	}
	requests := srv.batch["requests"].([]any)
	insert := requests[0].(map[string]any)["insertText"].(map[string]any)
	if insert["text"] != "\nNachtrag." {
		t.Fatalf("text = %q, want a leading newline so it is its own paragraph", insert["text"])
	}
}

func TestDocsWhoamiPrintsTheAddressToShareWith(t *testing.T) {
	srv := &docsTestServer{t: t}
	// A user cannot share a document with an address they cannot see, which is
	// what makes this the first command to run.
	_, out, err := runDocs(t, &State{OutputFormat: "text"}, srv.start(t), "whoami")
	if err != nil {
		t.Fatalf("docs whoami: %v", err)
	}
	if strings.TrimSpace(out) != "text-cli@p.iam.gserviceaccount.com" {
		t.Fatalf("output = %q, want just the address", out)
	}
}
