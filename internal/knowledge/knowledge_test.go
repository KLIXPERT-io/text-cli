package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

type fakeSource struct {
	name string
	art  *Article
	hits []SearchHit
	err  error
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Lookup(_ context.Context, _, _ string) (*Article, error) {
	return f.art, f.err
}

func (f *fakeSource) Search(_ context.Context, _, _ string, _ int) ([]SearchHit, error) {
	return f.hits, f.err
}

func TestRegisterAndOpen(t *testing.T) {
	want := &fakeSource{name: "fake-open"}
	Register("fake-open", func() (Source, error) { return want, nil })

	got, err := Open("fake-open")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != Source(want) {
		t.Fatalf("Open returned %#v, want the registered source", got)
	}
	// Lookup is case- and whitespace-insensitive, so a stray --source " Wikipedia "
	// is not an error the user has to debug.
	if _, err := Open("  FAKE-OPEN "); err != nil {
		t.Fatalf("Open with mixed case/whitespace: %v", err)
	}
	if !Registered("fake-open") {
		t.Fatal("Registered = false for a registered source")
	}
}

func TestOpenUnknownSourceListsKnownOnes(t *testing.T) {
	_, err := Open("encyclopedia-galactica")
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error is %T, want *errs.E", err)
	}
	if e.Code != errs.CodeInvalidArgs {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeInvalidArgs)
	}
	if !strings.Contains(e.Hint, SourceWikipedia) {
		t.Fatalf("hint %q does not list the known sources", e.Hint)
	}
}

func TestOpenFactoryErrorBecomesProviderUnavailable(t *testing.T) {
	Register("fake-broken", func() (Source, error) { return nil, errors.New("boom") })
	_, err := Open("fake-broken")
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeProviderUnavailable {
		t.Fatalf("error = %v, want provider_unavailable", err)
	}
}

func TestOpenFactoryStructuredErrorPassesThrough(t *testing.T) {
	Register("fake-noauth", func() (Source, error) {
		return nil, errs.New(errs.CodeAuthMissing, "nope")
	})
	_, err := Open("fake-noauth")
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeAuthMissing {
		t.Fatalf("error = %v, want auth_missing passed through", err)
	}
}

// TestWikipediaRegistersItself is the "register, don't wire" contract: the
// backend appears because its own init said so, not because a list mentions it.
func TestWikipediaRegistersItself(t *testing.T) {
	if !Registered(SourceWikipedia) {
		t.Fatalf("Names() = %v, want it to include %q", Names(), SourceWikipedia)
	}
	src, err := Open(SourceWikipedia)
	if err != nil {
		t.Fatalf("Open(wikipedia): %v", err)
	}
	if src.Name() != SourceWikipedia {
		t.Fatalf("Name = %q", src.Name())
	}
	if _, ok := src.(TimeoutSetter); !ok {
		t.Fatal("the wikipedia source must accept the command's --timeout")
	}
}

func TestNormalizeLang(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "en"},
		{" AUTO ", "en"},
		{"auto", "en"},
		{"en", "en"},
		{"DE", "de"},
		{"de-AT", "de"},
		{"pt_BR", "pt"},
		{"simple", "simple"},
		{"../etc", "en"},
		{"en.wikipedia.org", "en"},
	}
	for _, tc := range tests {
		if got := NormalizeLang(tc.in); got != tc.want {
			t.Fatalf("NormalizeLang(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalTitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Ada_Lovelace", "Ada Lovelace"},
		{"  Ada   Lovelace  ", "Ada Lovelace"},
		{"Große_Koalition", "Große Koalition"},
		{"", ""},
		{"   ", ""},
		// Case is load-bearing on Wikipedia and must survive.
		{"iPhone", "iPhone"},
	}
	for _, tc := range tests {
		if got := CanonicalTitle(tc.in); got != tc.want {
			t.Fatalf("CanonicalTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseWikipediaURL(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantTitle string
		wantLang  string
		wantOK    bool
	}{
		{"english article", "https://en.wikipedia.org/wiki/Ada_Lovelace", "Ada Lovelace", "en", true},
		{"german article", "https://de.wikipedia.org/wiki/Ada_Lovelace", "Ada Lovelace", "de", true},
		{"percent escaped", "https://de.wikipedia.org/wiki/Gro%C3%9Fe_Koalition", "Große Koalition", "de", true},
		{"mobile host", "https://de.m.wikipedia.org/wiki/Berlin", "Berlin", "de", true},
		{"www has no language", "https://www.wikipedia.org/wiki/Berlin", "Berlin", "en", true},
		{"another host", "https://example.com/wiki/Berlin", "", "", false},
		{"empty", "", "", "", false},
		{"no path", "https://en.wikipedia.org/", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			title, lang, ok := ParseWikipediaURL(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if title != tc.wantTitle || lang != tc.wantLang {
				t.Fatalf("= (%q, %q), want (%q, %q)", title, lang, tc.wantTitle, tc.wantLang)
			}
		})
	}
}

// TestAliasesDropDuplicates keeps the alias list from repeating the resolved
// title back at the caller in three spellings.
func TestAliasesDropDuplicates(t *testing.T) {
	got := aliases("Ada Lovelace", "Ada_Lovelace", "Ada Lovelace", "", "Countess of Lovelace", "Countess of Lovelace")
	if len(got) != 1 || got[0] != "Countess of Lovelace" {
		t.Fatalf("aliases = %v", got)
	}
}
