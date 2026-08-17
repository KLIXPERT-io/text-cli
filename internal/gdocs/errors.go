package gdocs

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"google.golang.org/api/googleapi"
)

// translate converts a Docs or Drive failure into a structured *errs.E.
//
// One mapping here matters more than all the others. **Google answers a
// document the caller cannot see with 404, not 403** — deliberately, so that an
// id cannot be probed for existence. Reported literally, that becomes "not
// found" for a document the user is looking at in another tab, and the user
// goes hunting for a typo that is not there. So a 404 on a well-formed id is
// reported as what it almost always is: the document has not been shared with
// the service account yet, and here is the address to share it with.
//
// The remaining distinction that earns its keep is 403-because-the-API-is-off
// versus 403-because-of-permissions. The first is fixed once, in the Cloud
// console, by the person who owns the key; the second is fixed per document, by
// whoever owns the document. Sending someone to the wrong one of those costs an
// afternoon.
func translate(err error, acct Account, write bool) error {
	if err == nil {
		return nil
	}
	var structured *errs.E
	if errors.As(err, &structured) {
		return structured
	}

	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return translateAPI(apiErr, acct, write)
	}
	return translateTransport(err)
}

func translateAPI(e *googleapi.Error, acct Account, write bool) error {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = http.StatusText(e.Code)
	}
	lower := strings.ToLower(msg + " " + reasons(e))

	switch {
	case e.Code == http.StatusNotFound:
		return errs.Newf(errs.CodeNotFound, "the document is not available to this account: %s", msg).
			WithHint("Google returns \"not found\" for a document that exists but has not been shared. " + shareHint(acct, write) +
				" If it really is the wrong id, check the URL.")

	case e.Code == http.StatusUnauthorized:
		return errs.Newf(errs.CodeAuthExpired, "the credentials were rejected: %s", msg).
			WithHint("The service account key may have been disabled or deleted. Check it at " + consoleServiceAccounts + ", or point --service-account at a current key.")

	case e.Code == http.StatusForbidden && apiDisabled(lower):
		return errs.Newf(errs.CodeAuthDenied, "an API is not enabled on the credential's project: %s", msg).
			WithHint("Enable both, once, on project " + projectOf(acct) + ": " + apiLibraryDocs + " and " + apiLibraryDrive + ".")

	case e.Code == http.StatusForbidden && strings.Contains(lower, "scope"):
		return errs.Newf(errs.CodeAuthDenied, "the credentials do not carry the required scopes: %s", msg).
			WithHint("Application Default Credentials from `gcloud auth application-default login` do not include the Docs and Drive scopes. Use a service account key: `text config set docs.service_account_path <key.json>`.")

	case e.Code == http.StatusForbidden:
		return errs.Newf(errs.CodeAuthDenied, "access to the document was denied: %s", msg).
			WithHint(shareHint(acct, write))

	case e.Code == http.StatusTooManyRequests:
		return errs.Newf(errs.CodeRateLimited, "Google rate limit: %s", msg).
			WithHint("Retry in a moment, or lower the number of documents in flight.").
			WithRetry(30)

	case e.Code == http.StatusBadRequest && strings.Contains(lower, "revision"):
		// The write was computed against a revision that is no longer current:
		// somebody edited the document between the read and the write. This is
		// exactly the collision requiredRevisionId exists to catch, so it is
		// reported as a conflict to re-run rather than as a malformed request.
		return errs.Newf(errs.CodeInvalidArgs, "the document changed since it was read: %s", msg).
			WithHint("Someone edited the document between the read and the write, so the edit was refused rather than applied to text that had moved. Re-run the command.").
			WithRetry(0)

	case e.Code == http.StatusBadRequest:
		return errs.Newf(errs.CodeInvalidArgs, "Google rejected the request: %s", msg).
			WithHint("Check the document id and the text being written.")

	case e.Code >= 500:
		return errs.Newf(errs.CodeAPI5xx, "Google failed on its side (%d): %s", e.Code, msg).
			WithHint("Retry; if it persists, check https://www.google.com/appsstatus.").
			WithRetry(30)
	}
	return errs.Newf(errs.CodeGeneric, "Google returned %d: %s", e.Code, msg)
}

// apiDisabled recognises the "this API has never been used in this project"
// response, which arrives as a 403 with a message rather than as its own code.
func apiDisabled(lower string) bool {
	return strings.Contains(lower, "has not been used in project") ||
		strings.Contains(lower, "api has not been enabled") ||
		strings.Contains(lower, "is disabled") ||
		strings.Contains(lower, "accessnotconfigured")
}

func reasons(e *googleapi.Error) string {
	parts := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		parts = append(parts, item.Reason)
	}
	return strings.Join(parts, " ")
}

func projectOf(acct Account) string {
	if acct.ProjectID == "" {
		return "the key's project"
	}
	return acct.ProjectID
}

// translateTransport classifies failures that never became an HTTP status: the
// credential resolution the client library does at construction, context
// deadlines, and plain network trouble. It mirrors internal/entity's transport
// mapping, because they fail the same way for the same reasons.
func translateTransport(err error) error {
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "could not find default credentials"),
		strings.Contains(lower, "google_application_credentials"),
		strings.Contains(lower, "no credentials"),
		strings.Contains(lower, "cannot read credentials"),
		strings.Contains(lower, "unable to read service account"):
		return errs.New(errs.CodeAuthMissing, "no Google credentials").
			WithHint("text docs authenticates as a service account. Create one at " + consoleServiceAccounts +
				", download a JSON key, enable the Docs and Drive APIs on its project, then run " +
				"`text config set docs.service_account_path <key.json>`.")

	case strings.Contains(lower, "oauth2: cannot fetch token"),
		strings.Contains(lower, "invalid_grant"):
		return errs.Newf(errs.CodeAuthDenied, "the service account key was rejected: %s", msg).
			WithHint("Confirm the key is still active at " + consoleServiceAccounts + ". A deleted key fails exactly like this.")

	case errors.Is(err, context.DeadlineExceeded):
		return errs.Newf(errs.CodeNetworkUnreachable, "the call timed out: %s", msg).
			WithHint("Raise the deadline with --timeout.").
			WithRetry(5)

	case errors.Is(err, context.Canceled):
		return errs.New(errs.CodeGeneric, "request cancelled")

	case strings.Contains(lower, "no such host"),
		strings.Contains(lower, "dial tcp"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "network is unreachable"):
		return errs.Newf(errs.CodeNetworkUnreachable, "could not reach docs.googleapis.com: %s", msg).
			WithHint("Check the network or proxy settings.").
			WithRetry(5)
	}
	return errs.New(errs.CodeGeneric, msg)
}
