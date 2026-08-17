package gdocs

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// Console links used in hints. An error that names the exact page saves the
// user a search; an error that says "permission denied" does not.
const (
	consoleServiceAccounts = "https://console.cloud.google.com/iam-admin/serviceaccounts"
	apiLibraryDocs         = "https://console.cloud.google.com/apis/library/docs.googleapis.com"
	apiLibraryDrive        = "https://console.cloud.google.com/apis/library/drive.googleapis.com"
)

// Options is what a client is opened with.
type Options struct {
	// ServiceAccountPath points at a Google service account key file. Empty
	// falls back to Application Default Credentials.
	ServiceAccountPath string
	// Write asks for read-write scopes. A read command leaves it false and gets
	// a token that cannot modify anything.
	Write bool
	// Comments asks for the Drive scopes on top of the Docs ones. Comments do
	// not exist in the Docs API, and a read of the document body should not
	// carry a Drive token it never uses.
	Comments bool
	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration
	// Endpoint overrides the API host, and drops authentication with it.
	//
	// There is no self-hosted Google Docs, so unlike the Firecrawl base URL
	// this exists for one reason: the tests point it at an httptest.Server so
	// the suite covers the request shapes, the response mapping, and the error
	// translation without a credential or a network.
	Endpoint string
}

// Scopes is the scope set the options resolve to. It is exported because
// `text docs whoami` reports it: "which document can I touch, and how" is
// answered by the scopes as much as by the sharing settings.
func (o Options) Scopes() []string {
	scopes := []string{ScopeDocumentsRead}
	if o.Write {
		scopes = []string{ScopeDocuments}
	}
	if o.Comments {
		if o.Write {
			scopes = append(scopes, ScopeDrive)
		} else {
			scopes = append(scopes, ScopeDriveRead)
		}
	}
	return scopes
}

// EffectiveTimeout returns the per-call timeout, defaulted.
func (o Options) EffectiveTimeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

// credentials is the part of a service account key file this package reads.
//
// The file is parsed locally rather than being handed straight to the token
// source because of one field: client_email. Every access failure in this
// package ends with "share the document with this address", and the address is
// in the key. Reading it here means the CLI can print it in the error that
// names the problem, instead of telling the user to go and find their key file.
type credentials struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	ProjectID   string `json:"project_id"`
}

// resolveKeyPath applies the credential precedence: the explicit path, then
// TEXT_SERVICE_ACCOUNT, then GOOGLE_APPLICATION_CREDENTIALS. An empty return
// means Application Default Credentials.
//
// This mirrors internal/entity's resolution deliberately: one machine, one
// Google credential, and a user who has already configured `text entities`
// should not have to configure a second thing to read a document. A path that
// was configured but does not exist is an error rather than a silent fallback,
// because falling back would turn a typo into a permission error much later.
func resolveKeyPath(explicit string) (path, source string, err error) {
	path, source = strings.TrimSpace(explicit), "--service-account"
	if path == "" {
		path, source = strings.TrimSpace(os.Getenv("TEXT_SERVICE_ACCOUNT")), "TEXT_SERVICE_ACCOUNT"
	}
	if path == "" {
		path, source = strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")), "GOOGLE_APPLICATION_CREDENTIALS"
	}
	if path == "" {
		return "", "application default credentials", nil
	}
	expanded := config.ExpandHome(path)
	if _, statErr := os.Stat(expanded); statErr != nil {
		return "", source, errs.Newf(errs.CodeAuthMissing, "service account key not found: %s (from %s)", expanded, source).
			WithHint("Point `text config set docs.service_account_path <path>` or --service-account at a service account key with the Docs and Drive APIs enabled, or unset it to use Application Default Credentials.")
	}
	return expanded, source, nil
}

// loadAccount reads the identity out of a key file.
//
// A file that parses but is not a service account key is reported as such: an
// OAuth client secret and a service account key are both JSON with a project in
// them, and downloading the wrong one from the console is a common mistake that
// otherwise surfaces as an unreadable token error.
func loadAccount(path, source string) (Account, error) {
	acct := Account{KeyPath: path, Source: source}
	if path == "" {
		return acct, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return acct, errs.Newf(errs.CodeAuthMissing, "read service account key: %s", err.Error()).
			WithHint("Check the path in `text config get docs.service_account_path` and that the file is readable.")
	}
	var creds credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return acct, errs.Newf(errs.CodeAuthMissing, "service account key is not valid JSON: %s", path).
			WithHint("Download a key from " + consoleServiceAccounts + " — Keys, Add key, JSON.")
	}
	if creds.Type != "" && creds.Type != "service_account" {
		return acct, errs.Newf(errs.CodeAuthMissing, "%s is a %q credential, not a service account key", path, creds.Type).
			WithHint("text docs authenticates as a service account. Create one at " + consoleServiceAccounts + " and download a JSON key.")
	}
	acct.Email, acct.ProjectID = creds.ClientEmail, creds.ProjectID
	return acct, nil
}

// shareHint is the sentence this whole feature turns on.
//
// A service account has no way to request access to a document and no consent
// screen to send the user to. The only thing that grants access is a human
// pasting the account's address into the document's Share dialog — so every
// 403 and every 404 ends here, with the address already printed. `text docs
// whoami` prints the same line on demand.
//
// It asks for Viewer on a read and Editor on a write because over-granting is
// the user's document to regret, not this CLI's to assume.
func shareHint(acct Account, write bool) string {
	role := "Viewer"
	if write {
		role = "Editor"
	}
	if acct.Email == "" {
		return "The document must be shared with the account this CLI authenticates as. " +
			"Application Default Credentials are in use, so the address is whichever account ran " +
			"`gcloud auth application-default login`. Point --service-account at a service account key to get a fixed, printable address."
	}
	return "Share the document with " + acct.Email + " as " + role +
		": open it in Google Docs, click Share, paste that address, pick " + role +
		", and untick \"Notify people\". `text docs whoami` prints the address again."
}
