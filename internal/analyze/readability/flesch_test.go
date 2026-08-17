package readability

import (
	"errors"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

func TestFleschFormula(t *testing.T) {
	tests := []struct {
		name  string
		asl   float64
		asw   float64
		score float64
		level string
		grade string
	}{
		{"typical prose", 10, 1.5, 69.8, "standard", "8th–9th grade"},
		{"short simple sentences", 5, 1.2, 100.2, "very easy", "5th grade"},
		{"college level", 20, 1.7, 42.7, "difficult", "college"},
		// Nothing clamps the score itself: a one-syllable-per-word,
		// one-word-per-sentence text really does exceed 100.
		{"above the scale", 1, 1, 121.2, "very easy", "5th grade"},
		// And a tortured 50-word sentence of three-syllable words really does
		// go far below zero. That number is information; we keep it.
		{"below the scale", 50, 3, -97.7, "very confusing", "college graduate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Flesch(mkDoc(textproc.LangEnglish, tt.asl, tt.asw))
			if err != nil {
				t.Fatalf("Flesch: %v", err)
			}
			if got.Score != tt.score {
				t.Fatalf("score = %v, want %v", got.Score, tt.score)
			}
			if got.Level != tt.level || got.Grade != tt.grade {
				t.Fatalf("level/grade = %q/%q, want %q/%q", got.Level, got.Grade, tt.level, tt.grade)
			}
			if got.Metric != "flesch" || got.Title != "Flesch Reading Ease" {
				t.Fatalf("metric/title = %q/%q", got.Metric, got.Title)
			}
			if got.Scale != "0–100, higher is easier" {
				t.Fatalf("scale = %q", got.Scale)
			}
			if got.Language != "en" {
				t.Fatalf("language = %q, want en", got.Language)
			}
		})
	}
}

// Every band edge, exercised through the real formula rather than through
// classify alone: an ASL solved to land the score exactly on the boundary must
// come out in the easier band.
func TestFleschBandEdges(t *testing.T) {
	const asw = 1.5
	edges := []struct {
		score float64
		level string
		grade string
	}{
		{90, "very easy", "5th grade"},
		{80, "easy", "6th grade"},
		{70, "fairly easy", "7th grade"},
		{60, "standard", "8th–9th grade"},
		{50, "fairly difficult", "10th–12th grade"},
		{30, "difficult", "college"},
		{0, "very confusing", "college graduate"},
	}
	for _, e := range edges {
		asl := (206.835 - 84.6*asw - e.score) / 1.015
		got, err := Flesch(mkDoc(textproc.LangEnglish, asl, asw))
		if err != nil {
			t.Fatalf("Flesch: %v", err)
		}
		if got.Score != e.score {
			t.Fatalf("solved for %v, got score %v", e.score, got.Score)
		}
		if got.Level != e.level || got.Grade != e.grade {
			t.Fatalf("at %v: level/grade = %q/%q, want %q/%q", e.score, got.Level, got.Grade, e.level, e.grade)
		}
	}
}

func TestFleschExtra(t *testing.T) {
	d := &textproc.Doc{
		Language: textproc.LangEnglish,
		Stats: textproc.Stats{
			Sentences:           3,
			Words:               42,
			Syllables:           78,
			AvgSentenceLength:   14,
			AvgSyllablesPerWord: 1.857142857,
		},
	}
	got, err := Flesch(d)
	if err != nil {
		t.Fatalf("Flesch: %v", err)
	}
	want := map[string]any{
		"asl":       14.0,
		"asw":       1.86,
		"words":     42,
		"sentences": 3,
		"syllables": 78,
	}
	for k, v := range want {
		if got.Extra[k] != v {
			t.Fatalf("extra[%q] = %v (%T), want %v (%T)", k, got.Extra[k], got.Extra[k], v, v)
		}
	}
}

// A Doc with no language still scores: the metric knows what it was calibrated
// for even when the caller did not say.
func TestFleschLanguageFallback(t *testing.T) {
	got, err := Flesch(mkDoc(textproc.LangAuto, 10, 1.5))
	if err != nil {
		t.Fatalf("Flesch: %v", err)
	}
	if got.Language != "en" {
		t.Fatalf("language = %q, want en", got.Language)
	}
}

func TestFleschEmptyDoc(t *testing.T) {
	for name, d := range map[string]*textproc.Doc{
		"zero doc":     {},
		"no words":     {Stats: textproc.Stats{Sentences: 3}},
		"no sentences": {Stats: textproc.Stats{Words: 12}},
		"nil":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Flesch(d)
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
