package readability

import (
	"errors"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/analyze"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// mkLixDoc builds a Doc with words per sentence and long words set directly.
// words is always 100, so longWords and the long-word percentage are the same
// number and the arithmetic in the comments stays checkable.
func mkLixDoc(lang textproc.Language, sentences, longWords int) *textproc.Doc {
	return &textproc.Doc{
		Language: lang,
		Stats: textproc.Stats{
			Sentences:         sentences,
			Words:             100,
			Syllables:         160,
			Characters:        550,
			LongWords:         longWords,
			AvgSentenceLength: 100 / float64(sentences),
		},
	}
}

func TestLIXFormula(t *testing.T) {
	tests := []struct {
		name      string
		sentences int
		longWords int
		score     float64
		level     string
		grade     string
	}{
		// LIX = words/sentences + 100×longWords/words.
		// 100/10 + 10 = 10 + 10 = 20
		{"children's book", 10, 10, 20, "very easy", "children's literature"},
		// 100/10 + 30 = 10 + 30 = 40
		{"normal prose", 10, 30, 40, "medium", "normal prose"},
		// 100/5 + 50 = 20 + 50 = 70
		{"academic", 5, 50, 70, "very difficult", "academic"},
		// 100/4 + 25 = 25 + 25 = 50
		{"technical", 4, 25, 50, "difficult", "technical, non-fiction"},
		// 100/8 + 22 = 12.5 + 22 = 34.5
		{"popular press", 8, 22, 34.5, "easy", "fiction, popular press"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LIX(mkLixDoc(textproc.LangEnglish, tt.sentences, tt.longWords))
			if err != nil {
				t.Fatalf("LIX: %v", err)
			}
			if got.Score != tt.score {
				t.Fatalf("score = %v, want %v", got.Score, tt.score)
			}
			if got.Level != tt.level || got.Grade != tt.grade {
				t.Fatalf("level/grade = %q/%q, want %q/%q", got.Level, got.Grade, tt.level, tt.grade)
			}
			if got.Metric != "lix" || got.Title != "LIX (Läsbarhetsindex)" {
				t.Fatalf("metric/title = %q/%q", got.Metric, got.Title)
			}
			if got.Scale != lixScale {
				t.Fatalf("scale = %q, want %q", got.Scale, lixScale)
			}
		})
	}
}

// The band edges, solved through the real formula. LIX's published table is
// written "<25 very easy, 25–35 easy", so unlike the Wiener Sachtextformel the
// boundary belongs to the HARDER band: exactly 25.0 is "easy".
func TestLIXBandEdges(t *testing.T) {
	edges := []struct {
		sentences int
		longWords int
		score     float64
		level     string
	}{
		// 100/10 + 14 = 24 — just under the first edge.
		{10, 14, 24, "very easy"},
		// 100/10 + 15 = 25 — on it, and therefore already "easy".
		{10, 15, 25, "easy"},
		// 100/10 + 24 = 34
		{10, 24, 34, "easy"},
		// 100/10 + 25 = 35
		{10, 25, 35, "medium"},
		// 100/10 + 34 = 44
		{10, 34, 44, "medium"},
		// 100/10 + 35 = 45
		{10, 35, 45, "difficult"},
		// 100/10 + 44 = 54
		{10, 44, 54, "difficult"},
		// 100/10 + 45 = 55
		{10, 45, 55, "very difficult"},
	}
	for _, e := range edges {
		got, err := LIX(mkLixDoc(textproc.LangEnglish, e.sentences, e.longWords))
		if err != nil {
			t.Fatalf("LIX: %v", err)
		}
		if got.Score != e.score {
			t.Fatalf("score = %v, want %v", got.Score, e.score)
		}
		if got.Level != e.level {
			t.Fatalf("at %v: level = %q, want %q", e.score, got.Level, e.level)
		}
	}
}

// LIX is the one formula here that scores a language nobody calibrated for, and
// it must report the language it was actually run on.
func TestLIXIsLanguageAgnostic(t *testing.T) {
	m, ok := analyze.Get("lix")
	if !ok {
		t.Fatal("lix is not registered")
	}
	for _, lang := range []textproc.Language{textproc.LangEnglish, textproc.LangGerman, "fr", "sv"} {
		if !m.Supports(lang) {
			t.Fatalf("lix should support %q", lang)
		}
	}

	de, err := LIX(mkLixDoc(textproc.LangGerman, 10, 30))
	if err != nil {
		t.Fatalf("LIX: %v", err)
	}
	if de.Language != "de" {
		t.Fatalf("language = %q, want de", de.Language)
	}
	// A Doc built without a language claims none: LIX is calibrated for none.
	none, err := LIX(mkLixDoc(textproc.LangAuto, 10, 30))
	if err != nil {
		t.Fatalf("LIX: %v", err)
	}
	if none.Language != analyze.AnyLanguage {
		t.Fatalf("language = %q, want %q", none.Language, analyze.AnyLanguage)
	}
}

func TestLIXExtra(t *testing.T) {
	got, err := LIX(mkLixDoc(textproc.LangEnglish, 10, 30))
	if err != nil {
		t.Fatalf("LIX: %v", err)
	}
	want := map[string]any{
		"asl":        10.0,
		"iw":         30.0,
		"long_words": 30,
		"words":      100,
		"sentences":  10,
	}
	for k, v := range want {
		if got.Extra[k] != v {
			t.Fatalf("extra[%q] = %v (%T), want %v (%T)", k, got.Extra[k], got.Extra[k], v, v)
		}
	}
}

func TestLIXEmptyDoc(t *testing.T) {
	for name, d := range map[string]*textproc.Doc{
		"zero doc":     {},
		"no words":     {Stats: textproc.Stats{Sentences: 3}},
		"no sentences": {Stats: textproc.Stats{Words: 12}},
		"nil":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LIX(d)
			if err == nil {
				t.Fatal("want an error for an unmeasurable document")
			}
			var e *errs.E
			if !errors.As(err, &e) {
				t.Fatalf("error is %T, want *errs.E", err)
			}
			if e.Code != errs.CodeEmptyInput {
				t.Fatalf("code = %q, want %q", e.Code, errs.CodeEmptyInput)
			}
			if errs.ExitCode(err) != 5 {
				t.Fatalf("exit code = %d, want 5", errs.ExitCode(err))
			}
		})
	}
}
