package entity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cloud.google.com/go/language/apiv1/languagepb"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTranslateStatusCodes(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantCode  errs.Code
		retriable bool
		retryAfte int
		hintPart  string
	}{
		{
			name:     "unauthenticated",
			err:      status.Error(codes.Unauthenticated, "request had invalid authentication credentials"),
			wantCode: errs.CodeAuthExpired,
			hintPart: "gcloud auth application-default login",
		},
		{
			name:     "permission denied",
			err:      status.Error(codes.PermissionDenied, "Cloud Natural Language API has not been used in project 123"),
			wantCode: errs.CodeAuthDenied,
			hintPart: "language.googleapis.com",
		},
		{
			name:      "resource exhausted",
			err:       status.Error(codes.ResourceExhausted, "Quota exceeded for quota metric 'Requests'"),
			wantCode:  errs.CodeQuotaExceeded,
			retriable: true,
			retryAfte: 60,
			hintPart:  "quota",
		},
		{
			name:     "invalid argument",
			err:      status.Error(codes.InvalidArgument, "One of content, or gcs_content_uri must be set"),
			wantCode: errs.CodeInvalidArgs,
			hintPart: "input",
		},
		{
			name:     "invalid argument about language",
			err:      status.Error(codes.InvalidArgument, "The language sw is not supported for document_sentiment analysis"),
			wantCode: errs.CodeUnsupportedLanguage,
			hintPart: "https://cloud.google.com/natural-language/docs/languages",
		},
		{
			name:      "unavailable",
			err:       status.Error(codes.Unavailable, "connection closed"),
			wantCode:  errs.CodeNetworkUnreachable,
			retriable: true,
			retryAfte: 5,
			hintPart:  "timeout",
		},
		{
			name:      "deadline exceeded",
			err:       status.Error(codes.DeadlineExceeded, "context deadline exceeded"),
			wantCode:  errs.CodeNetworkUnreachable,
			retriable: true,
			retryAfte: 5,
		},
		{
			name:      "internal",
			err:       status.Error(codes.Internal, "internal error"),
			wantCode:  errs.CodeAPI5xx,
			retriable: true,
			retryAfte: 30,
		},
		{
			name:      "unknown",
			err:       status.Error(codes.Unknown, "unknown error"),
			wantCode:  errs.CodeAPI5xx,
			retriable: true,
			retryAfte: 30,
		},
		{
			name:      "data loss",
			err:       status.Error(codes.DataLoss, "data loss"),
			wantCode:  errs.CodeAPI5xx,
			retriable: true,
			retryAfte: 30,
		},
		{
			name:     "not found",
			err:      status.Error(codes.NotFound, "not found"),
			wantCode: errs.CodeNotFound,
		},
		{
			name:     "missing default credentials",
			err:      errors.New("google: could not find default credentials. See https://cloud.google.com/docs/authentication/external/set-up-adc for more information"),
			wantCode: errs.CodeAuthMissing,
			hintPart: "--service-account",
		},
		{
			name:     "token rejected",
			err:      errors.New(`oauth2: cannot fetch token: 400 Bad Request {"error":"invalid_grant"}`),
			wantCode: errs.CodeAuthDenied,
			hintPart: "key was rejected",
		},
		{
			name:      "dns failure",
			err:       errors.New("dial tcp: lookup language.googleapis.com: no such host"),
			wantCode:  errs.CodeNetworkUnreachable,
			retriable: true,
			retryAfte: 5,
		},
		{
			name:     "unclassified",
			err:      errors.New("something else entirely"),
			wantCode: errs.CodeGeneric,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Translate(tc.err)
			var e *errs.E
			if !errors.As(got, &e) {
				t.Fatalf("Translate returned %T (%v), want *errs.E", got, got)
			}
			if e.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", e.Code, tc.wantCode)
			}
			if e.Retriable != tc.retriable {
				t.Fatalf("retriable = %v, want %v", e.Retriable, tc.retriable)
			}
			if e.RetryAfterSec != tc.retryAfte {
				t.Fatalf("retry_after_sec = %d, want %d", e.RetryAfterSec, tc.retryAfte)
			}
			if tc.hintPart != "" && !strings.Contains(strings.ToLower(e.Hint), strings.ToLower(tc.hintPart)) {
				t.Fatalf("hint %q does not mention %q", e.Hint, tc.hintPart)
			}
			if e.Message == "" {
				t.Fatal("message is empty; the API's own wording must survive translation")
			}
		})
	}
}

func TestTranslateNilAndPassthrough(t *testing.T) {
	if got := Translate(nil); got != nil {
		t.Fatalf("Translate(nil) = %v, want nil", got)
	}
	in := errs.New(errs.CodeAuthMissing, "already structured")
	if got := Translate(in); got != error(in) {
		t.Fatalf("Translate re-wrapped an *errs.E: %v", got)
	}
	if got := Translate(fmt.Errorf("wrapped: %w", status.Error(codes.PermissionDenied, "denied"))); errs.ExitCode(got) != 2 {
		t.Fatalf("wrapped status error lost its code: %v", got)
	}
}

func TestTranslateContextDeadline(t *testing.T) {
	got := Translate(fmt.Errorf("analyze: %w", context.DeadlineExceeded))
	var e *errs.E
	if !errors.As(got, &e) || e.Code != errs.CodeNetworkUnreachable || !e.Retriable {
		t.Fatalf("context deadline = %v, want retriable network_unreachable", got)
	}
}

// TestConvertResponse pins the v1 mapping: salience is carried through,
// language comes from the v1 `language` field, and language_supported is true
// because v1 has no such flag and refuses unsupported languages outright.
func TestConvertResponse(t *testing.T) {
	resp := &languagepb.AnalyzeEntitiesResponse{
		Language: "de",
		Entities: []*languagepb.Entity{
			{
				Name:     "Ada Lovelace",
				Type:     languagepb.Entity_PERSON,
				Salience: 0.6421,
				Metadata: map[string]string{
					"wikipedia_url": "https://de.wikipedia.org/wiki/Ada_Lovelace",
					"mid":           "/m/0ff4d",
				},
				Mentions: []*languagepb.EntityMention{
					{
						Text: &languagepb.TextSpan{Content: "Ada Lovelace", BeginOffset: 0},
						Type: languagepb.EntityMention_PROPER,
					},
					{
						Text: &languagepb.TextSpan{Content: "Lovelace", BeginOffset: 42},
						Type: languagepb.EntityMention_PROPER,
					},
				},
				Sentiment: &languagepb.Sentiment{Score: 0.5, Magnitude: 1.5},
			},
			{
				Name:     "Rechenmaschine",
				Type:     languagepb.Entity_OTHER,
				Salience: 0.1,
				Mentions: []*languagepb.EntityMention{
					{Text: &languagepb.TextSpan{Content: "Rechenmaschine", BeginOffset: 10}, Type: languagepb.EntityMention_COMMON},
				},
			},
		},
	}

	got := convertResponse(resp)
	if got.Provider != ProviderGoogle {
		t.Fatalf("provider = %q", got.Provider)
	}
	if got.Language != "de" || !got.LanguageSupported {
		t.Fatalf("language = %q supported = %v", got.Language, got.LanguageSupported)
	}
	if len(got.Entities) != 2 {
		t.Fatalf("entities = %d, want 2", len(got.Entities))
	}

	ada := got.Entities[0]
	if ada.Name != "Ada Lovelace" || ada.Type != "PERSON" {
		t.Fatalf("entity = %+v", ada)
	}
	// Salience survives the float32 -> float64 widening as an exact 4-decimal
	// number rather than 0.6420999765396118.
	if ada.Salience != 0.6421 {
		t.Fatalf("salience = %v, want 0.6421", ada.Salience)
	}
	if ada.Score() != 0.6421 {
		t.Fatalf("score = %v, want the salience", ada.Score())
	}
	// v1 has no per-mention probability; inventing one from salience would
	// confuse importance with confidence.
	if ada.Probability != 0 {
		t.Fatalf("probability = %v, want 0: v1 reports none", ada.Probability)
	}
	if ada.Mentions[0].Probability != 0 {
		t.Fatalf("mention probability = %v, want 0", ada.Mentions[0].Probability)
	}
	if ada.MentionCount != 2 || len(ada.Mentions) != 2 {
		t.Fatalf("mention_count = %d, mentions = %d", ada.MentionCount, len(ada.Mentions))
	}
	if ada.Mentions[1].BeginOffset != 42 || ada.Mentions[1].Type != "PROPER" || ada.Mentions[1].Text != "Lovelace" {
		t.Fatalf("mention = %+v", ada.Mentions[1])
	}
	if ada.WikipediaURL != "https://de.wikipedia.org/wiki/Ada_Lovelace" {
		t.Fatalf("wikipedia_url = %q, want it lifted out of metadata", ada.WikipediaURL)
	}
	if ada.MID != "/m/0ff4d" {
		t.Fatalf("mid = %q, want it lifted out of metadata", ada.MID)
	}
	if ada.Metadata["wikipedia_url"] == "" {
		t.Fatal("metadata must still be passed through as-is")
	}
	if ada.Sentiment == nil || ada.Sentiment.Score != 0.5 || ada.Sentiment.Magnitude != 1.5 {
		t.Fatalf("sentiment = %+v", ada.Sentiment)
	}

	other := got.Entities[1]
	if other.Type != "OTHER" || other.MentionCount != 1 || other.Mentions[0].Type != "COMMON" {
		t.Fatalf("entity = %+v", other)
	}
	if other.Salience != 0.1 {
		t.Fatalf("salience = %v, want 0.1", other.Salience)
	}
	if other.Sentiment != nil {
		t.Fatalf("sentiment = %+v, want nil when the provider returned none", other.Sentiment)
	}
	if other.WikipediaURL != "" || other.MID != "" || other.Metadata != nil {
		t.Fatalf("empty metadata must stay empty: %+v", other)
	}
}

func TestConvertResponseIsNilSafe(t *testing.T) {
	got := convertResponse(nil)
	if got == nil || got.Entities == nil || len(got.Entities) != 0 {
		t.Fatalf("convertResponse(nil) = %+v, want an empty result", got)
	}
	if !got.LanguageSupported {
		t.Fatal("language_supported must default to true for a backend without the signal")
	}
	got = convertResponse(&languagepb.AnalyzeEntitiesResponse{
		Entities: []*languagepb.Entity{
			nil,
			{Name: "bare", Mentions: []*languagepb.EntityMention{nil, {}}},
		},
	})
	if len(got.Entities) != 1 {
		t.Fatalf("nil entity was not skipped: %+v", got.Entities)
	}
	e := got.Entities[0]
	if e.Type != "UNKNOWN" || e.MentionCount != 1 || e.Probability != 0 || e.Salience != 0 {
		t.Fatalf("bare entity = %+v", e)
	}
	if e.Mentions[0].Text != "" || e.Mentions[0].BeginOffset != 0 {
		t.Fatalf("nil text span must degrade to zero values: %+v", e.Mentions[0])
	}
}

func TestResolveCredentialsPrecedence(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit.json")
	fromEnv := filepath.Join(dir, "text-sa.json")
	fromGoogle := filepath.Join(dir, "gac.json")
	for _, p := range []string{explicit, fromEnv, fromGoogle} {
		if err := os.WriteFile(p, []byte(`{"type":"service_account"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("TEXT_SERVICE_ACCOUNT", fromEnv)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", fromGoogle)

	if got, err := resolveCredentials(explicit); err != nil || got != explicit {
		t.Fatalf("explicit path: got %q err %v", got, err)
	}
	if got, err := resolveCredentials(""); err != nil || got != fromEnv {
		t.Fatalf("TEXT_SERVICE_ACCOUNT: got %q err %v", got, err)
	}

	t.Setenv("TEXT_SERVICE_ACCOUNT", "")
	if got, err := resolveCredentials(""); err != nil || got != fromGoogle {
		t.Fatalf("GOOGLE_APPLICATION_CREDENTIALS: got %q err %v", got, err)
	}

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	if got, err := resolveCredentials(""); err != nil || got != "" {
		t.Fatalf("no credentials must fall through to ADC: got %q err %v", got, err)
	}
}

func TestResolveCredentialsMissingFile(t *testing.T) {
	t.Setenv("TEXT_SERVICE_ACCOUNT", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	_, err := resolveCredentials(filepath.Join(t.TempDir(), "nope.json"))
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error is %T, want *errs.E", err)
	}
	if e.Code != errs.CodeAuthMissing {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeAuthMissing)
	}
	if !strings.Contains(e.Hint, "entities.service_account_path") || !strings.Contains(e.Hint, "--service-account") {
		t.Fatalf("hint %q must name the config key and the flag", e.Hint)
	}
}

func TestGoogleProviderName(t *testing.T) {
	p, err := Open(ProviderGoogle)
	if err != nil {
		t.Fatalf("Open(google): %v", err)
	}
	if p.Name() != ProviderGoogle {
		t.Fatalf("Name = %q", p.Name())
	}
	// Opening must not have created a client: --help pays nothing.
	g, ok := p.(*googleProvider)
	if !ok {
		t.Fatalf("provider is %T", p)
	}
	if g.clientInit || g.client != nil {
		t.Fatal("the factory eagerly built a gRPC client")
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close on an unused provider: %v", err)
	}
}
