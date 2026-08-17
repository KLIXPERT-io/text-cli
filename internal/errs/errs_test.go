package errs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The exit-code table is a published contract: README, SKILL.md, and any agent
// branching on `$?` depend on these exact numbers.
func TestExitCode(t *testing.T) {
	tests := []struct {
		code Code
		want int
	}{
		{CodeAuthMissing, 2},
		{CodeAuthExpired, 2},
		{CodeAuthDenied, 2},
		{CodeQuotaExceeded, 3},
		{CodeRateLimited, 3},
		{CodeNotFound, 4},
		{CodeInvalidArgs, 5},
		{CodeEmptyInput, 5},
		{CodeUnsupportedLanguage, 5},
		{CodeUnknownMetric, 5},
		{CodeNetworkUnreachable, 6},
		{CodeAPI5xx, 6},
		{CodeProviderUnavailable, 6},
		{CodeGeneric, 1},
	}
	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			if got := ExitCode(New(tt.code, "boom")); got != tt.want {
				t.Errorf("ExitCode(%s) = %d, want %d", tt.code, got, tt.want)
			}
		})
	}
}

func TestExitCodeNil(t *testing.T) {
	if got := ExitCode(nil); got != 0 {
		t.Errorf("ExitCode(nil) = %d, want 0", got)
	}
}

// A plain error from anywhere in the stack must still exit non-zero.
func TestExitCodePlainError(t *testing.T) {
	if got := ExitCode(fmt.Errorf("something broke")); got != 1 {
		t.Errorf("ExitCode(plain) = %d, want 1", got)
	}
}

// Wrapped errors must keep their code — the CLI wraps freely on the way up.
func TestExitCodeWrapped(t *testing.T) {
	wrapped := fmt.Errorf("calling provider: %w", New(CodeQuotaExceeded, "over budget"))
	if got := ExitCode(wrapped); got != 3 {
		t.Errorf("ExitCode(wrapped) = %d, want 3", got)
	}
}

// Errors go to stderr as exactly one JSON line, whatever --output says.
func TestWriteIsOneJSONLine(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf, New(CodeUnsupportedLanguage, "flesch does not support de").
		WithHint("Use --metrics amstad for German."))

	out := buf.String()
	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 {
		t.Fatalf("want a single line, got:\n%s", out)
	}

	var payload struct {
		Error *E `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("stderr is not valid JSON: %v", err)
	}
	if payload.Error.Code != CodeUnsupportedLanguage {
		t.Errorf("code = %q", payload.Error.Code)
	}
	if payload.Error.Hint == "" {
		t.Error("hint was dropped")
	}
	if payload.Error.Retriable {
		t.Error("retriable should default to false")
	}
}

func TestWritePlainError(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf, fmt.Errorf("unexpected"))
	var payload struct {
		Error *E `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	// An un-coded error must still arrive machine-readable, not as bare prose.
	if payload.Error.Code != CodeGeneric {
		t.Errorf("code = %q, want %q", payload.Error.Code, CodeGeneric)
	}
}

func TestWriteNil(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("wrote %q for a nil error", buf.String())
	}
}

func TestWithRetry(t *testing.T) {
	e := New(CodeRateLimited, "slow down").WithRetry(60)
	if !e.Retriable || e.RetryAfterSec != 60 {
		t.Errorf("got retriable=%v after=%d", e.Retriable, e.RetryAfterSec)
	}
}

func TestErrorString(t *testing.T) {
	if got := New(CodeNotFound, "no such file").Error(); got != "not_found: no such file" {
		t.Errorf("Error() = %q", got)
	}
}
