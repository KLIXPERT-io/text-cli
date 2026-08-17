package entity

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"

	language "cloud.google.com/go/language/apiv2"
	"cloud.google.com/go/language/apiv2/languagepb"
	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ProviderGoogle is the registry name of the Cloud Natural Language backend.
const ProviderGoogle = "google"

// Links used in hints. An error that names the exact console page saves the
// user a search; an error that just says "permission denied" does not.
const (
	docsLanguages = "https://cloud.google.com/natural-language/docs/languages"
	apiLibrary    = "https://console.cloud.google.com/apis/library/language.googleapis.com"
	adcSetup      = "https://cloud.google.com/docs/authentication/external/set-up-adc"
)

func init() {
	Register(ProviderGoogle, func() (Provider, error) { return &googleProvider{}, nil })
}

// googleProvider wraps the Cloud Natural Language API v2.
//
// The client is created on first use, not in the factory: constructing it opens
// a gRPC connection and resolves credentials, and `text entities --help` should
// pay for neither.
type googleProvider struct {
	mu sync.Mutex
	// client and clientPath are the memoized client and the credentials path it
	// was built with, so a caller that changes --service-account mid-process
	// gets a client that matches.
	client     *language.Client
	clientPath string
	clientInit bool
}

func (p *googleProvider) Name() string { return ProviderGoogle }

func (p *googleProvider) AnalyzeEntities(ctx context.Context, text string, opts Options) (*Result, error) {
	c, err := p.clientFor(ctx, opts)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, opts.EffectiveTimeout())
	defer cancel()

	resp, err := c.AnalyzeEntities(ctx, &languagepb.AnalyzeEntitiesRequest{
		Document: &languagepb.Document{
			Type:   languagepb.Document_PLAIN_TEXT,
			Source: &languagepb.Document_Content{Content: text},
			// An empty LanguageCode asks the API to detect the language.
			LanguageCode: opts.Language,
		},
		// UTF8 makes mention offsets byte offsets into the text we sent, which
		// is what a Go caller can slice with directly.
		EncodingType: languagepb.EncodingType_UTF8,
	})
	if err != nil {
		return nil, Translate(err)
	}
	return convertResponse(resp), nil
}

// Close releases the gRPC connection. The command type-asserts for io.Closer,
// so a provider without a connection does not have to implement this.
func (p *googleProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		return nil
	}
	err := p.client.Close()
	p.client, p.clientInit = nil, false
	return err
}

// clientFor returns the memoized client, building it on first use.
func (p *googleProvider) clientFor(ctx context.Context, opts Options) (*language.Client, error) {
	path, err := resolveCredentials(opts.ServiceAccountPath)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clientInit && p.clientPath == path {
		return p.client, nil
	}
	if p.client != nil {
		_ = p.client.Close()
		p.client, p.clientInit = nil, false
	}

	var clientOpts []option.ClientOption
	if path != "" {
		clientOpts = append(clientOpts, option.WithCredentialsFile(path))
	}
	c, err := language.NewClient(ctx, clientOpts...)
	if err != nil {
		return nil, Translate(err)
	}
	p.client, p.clientPath, p.clientInit = c, path, true
	return c, nil
}

// resolveCredentials picks the service account key file, highest priority
// first: the explicit option, TEXT_SERVICE_ACCOUNT, then
// GOOGLE_APPLICATION_CREDENTIALS. An empty return means "use Application
// Default Credentials" — gcloud's own login, a metadata server on GCE, or a
// workload identity — which is the right answer on a properly configured
// machine and needs no config at all.
//
// A path that was configured but does not exist is an error rather than a
// silent fallback to ADC: falling back would turn a typo into a confusing
// permission error much later.
func resolveCredentials(explicit string) (string, error) {
	path, source := explicit, "--service-account"
	if path == "" {
		path, source = os.Getenv("TEXT_SERVICE_ACCOUNT"), "TEXT_SERVICE_ACCOUNT"
	}
	if path == "" {
		path, source = os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"), "GOOGLE_APPLICATION_CREDENTIALS"
	}
	if path == "" {
		return "", nil
	}
	expanded := config.ExpandHome(path)
	if _, err := os.Stat(expanded); err != nil {
		return "", errs.Newf(errs.CodeAuthMissing, "service account key not found: %s (from %s)", expanded, source).
			WithHint("Point `text config set entities.service_account_path <path>` or --service-account at a Google service account key with access to the Cloud Natural Language API, or unset it to use Application Default Credentials.")
	}
	return expanded, nil
}

// Translate converts a Cloud Natural Language error into a structured *errs.E.
//
// Callers branch on the code, never on the message, so every failure mode the
// API has must land on a code — including the ones that never reach the wire,
// like a missing credential at client construction.
func Translate(err error) error {
	if err == nil {
		return nil
	}
	var e *errs.E
	if errors.As(err, &e) {
		return e
	}

	// ok is false for anything that never became a gRPC status; those are
	// credential and network failures, handled below.
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		msg := st.Message()
		lower := strings.ToLower(msg)
		switch st.Code() {
		case codes.Unauthenticated:
			return errs.New(errs.CodeAuthExpired, msg).
				WithHint("The credentials were rejected. Refresh the service account key (`text config set entities.service_account_path <path>`) or re-run `gcloud auth application-default login`.")

		case codes.PermissionDenied:
			return errs.New(errs.CodeAuthDenied, msg).
				WithHint("Enable the Cloud Natural Language API on the credential's project (" + apiLibrary + ") and grant the service account access to it.")

		case codes.ResourceExhausted:
			return errs.New(errs.CodeQuotaExceeded, msg).
				WithHint("The project's Natural Language quota is used up. Wait for the window to reset or raise the quota in the Cloud console.").
				WithRetry(60)

		case codes.InvalidArgument:
			// An unsupported language arrives as InvalidArgument, and it is the
			// single most common way this call fails on real text — worth its
			// own code so an agent can retry with --lang en instead of giving up.
			if strings.Contains(lower, "language") {
				return errs.New(errs.CodeUnsupportedLanguage, msg).
					WithHint("The document language is not supported. Pass a supported --lang, or omit it to let the API detect one: " + docsLanguages)
			}
			return errs.New(errs.CodeInvalidArgs, msg).
				WithHint("Check the input text and flags. Empty or binary input is rejected by the API.")

		case codes.Unavailable, codes.DeadlineExceeded:
			return errs.New(errs.CodeNetworkUnreachable, msg).
				WithHint("The API was unreachable or the call timed out. Check connectivity, or raise --timeout.").
				WithRetry(5)

		case codes.Internal, codes.Unknown, codes.DataLoss:
			return errs.New(errs.CodeAPI5xx, msg).
				WithHint("The API failed on its side. Retry; if it persists, check https://status.cloud.google.com.").
				WithRetry(30)

		case codes.NotFound:
			return errs.New(errs.CodeNotFound, msg)
		}
	}

	return translateTransport(err)
}

// translateTransport classifies errors that never became a gRPC status: the
// credential resolution failures the client library reports at construction,
// context deadlines, and plain network trouble.
func translateTransport(err error) error {
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "could not find default credentials"),
		strings.Contains(lower, "google_application_credentials"),
		strings.Contains(lower, "no credentials"),
		strings.Contains(lower, "cannot read credentials"),
		strings.Contains(lower, "unable to read service account"):
		return errs.New(errs.CodeAuthMissing, msg).
			WithHint("Set up credentials: `text config set entities.service_account_path <key.json>`, pass --service-account, or run `gcloud auth application-default login` (" + adcSetup + ").")

	case strings.Contains(lower, "oauth2: cannot fetch token"),
		strings.Contains(lower, "invalid_grant"):
		return errs.New(errs.CodeAuthDenied, msg).
			WithHint("The service account key was rejected. Confirm the key is still active and that the Cloud Natural Language API is enabled on its project.")

	case errors.Is(err, context.DeadlineExceeded):
		return errs.New(errs.CodeNetworkUnreachable, msg).
			WithHint("The call exceeded --timeout. Raise it, or shorten the document.").
			WithRetry(5)

	case errors.Is(err, context.Canceled):
		return errs.New(errs.CodeGeneric, msg)

	case strings.Contains(lower, "no such host"),
		strings.Contains(lower, "dial tcp"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "network is unreachable"):
		return errs.New(errs.CodeNetworkUnreachable, msg).
			WithHint("Could not reach language.googleapis.com. Check the network or proxy settings.").
			WithRetry(5)
	}
	return errs.New(errs.CodeGeneric, msg)
}

// convertResponse maps the protobuf response onto the neutral domain types.
// It is pure and total — nil-safe on every nested pointer — so the mapping is
// unit-testable without a network or a credential.
func convertResponse(resp *languagepb.AnalyzeEntitiesResponse) *Result {
	out := &Result{Provider: ProviderGoogle, Entities: []Entity{}}
	if resp == nil {
		return out
	}
	out.Language = resp.GetLanguageCode()
	out.LanguageSupported = resp.GetLanguageSupported()
	for _, pe := range resp.GetEntities() {
		if pe == nil {
			continue
		}
		out.Entities = append(out.Entities, convertEntity(pe))
	}
	return out
}

func convertEntity(pe *languagepb.Entity) Entity {
	e := Entity{
		Name: pe.GetName(),
		Type: pe.GetType().String(),
	}
	if md := pe.GetMetadata(); len(md) > 0 {
		e.Metadata = make(map[string]string, len(md))
		for k, v := range md {
			e.Metadata[k] = v
		}
	}
	e.LiftMetadata()

	for _, pm := range pe.GetMentions() {
		if pm == nil {
			continue
		}
		m := Mention{
			Text:        pm.GetText().GetContent(),
			Type:        pm.GetType().String(),
			BeginOffset: int(pm.GetText().GetBeginOffset()),
			Probability: float64(pm.GetProbability()),
		}
		// v2 dropped salience, so the only confidence signal left is per
		// mention. The strongest mention is the entity's probability.
		if m.Probability > e.Probability {
			e.Probability = m.Probability
		}
		e.Mentions = append(e.Mentions, m)
	}
	e.MentionCount = len(e.Mentions)

	if s := pe.GetSentiment(); s != nil {
		e.Sentiment = &Sentiment{Score: float64(s.GetScore()), Magnitude: float64(s.GetMagnitude())}
	}
	return e
}
