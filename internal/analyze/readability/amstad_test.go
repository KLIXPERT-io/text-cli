package readability

import (
	"errors"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

func TestAmstadFormula(t *testing.T) {
	tests := []struct {
		name  string
		asl   float64
		asw   float64
		score float64
		level string
		grade string
	}{
		{"typical prose", 10, 1.6, 76.4, "mittelleicht", "7th grade"},
		{"newspaper german", 14, 1.857, 57.4, "mittelschwer", "10th–12th grade"},
		{"long sentences", 20, 1.4, 78.1, "mittelleicht", "7th grade"},
		{"above the scale", 5, 1, 116.5, "sehr leicht", "4th grade"},
		{"below the scale", 60, 3, -55.5, "sehr schwer", "akademisch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Amstad(mkDoc(textproc.LangGerman, tt.asl, tt.asw))
			if err != nil {
				t.Fatalf("Amstad: %v", err)
			}
			if got.Score != tt.score {
				t.Fatalf("score = %v, want %v", got.Score, tt.score)
			}
			if got.Level != tt.level || got.Grade != tt.grade {
				t.Fatalf("level/grade = %q/%q, want %q/%q", got.Level, got.Grade, tt.level, tt.grade)
			}
			if got.Metric != "amstad" || got.Title != "Amstad (Flesch für Deutsch)" {
				t.Fatalf("metric/title = %q/%q", got.Metric, got.Title)
			}
			if got.Scale != "0–100, höher ist leichter" {
				t.Fatalf("scale = %q", got.Scale)
			}
			if got.Language != "de" {
				t.Fatalf("language = %q, want de", got.Language)
			}
		})
	}
}

func TestAmstadBandEdges(t *testing.T) {
	const asw = 1.6
	edges := []struct {
		score float64
		level string
		grade string
	}{
		{90, "sehr leicht", "4th grade"},
		{80, "leicht", "5th–6th grade"},
		{70, "mittelleicht", "7th grade"},
		{60, "mittel", "8th–9th grade"},
		{50, "mittelschwer", "10th–12th grade"},
		{30, "schwer", "Studium"},
		{0, "sehr schwer", "akademisch"},
	}
	for _, e := range edges {
		asl := 180 - 58.5*asw - e.score
		got, err := Amstad(mkDoc(textproc.LangGerman, asl, asw))
		if err != nil {
			t.Fatalf("Amstad: %v", err)
		}
		if got.Score != e.score {
			t.Fatalf("solved for %v, got score %v", e.score, got.Score)
		}
		if got.Level != e.level || got.Grade != e.grade {
			t.Fatalf("at %v: level/grade = %q/%q, want %q/%q", e.score, got.Level, got.Grade, e.level, e.grade)
		}
	}
}

// The whole reason Amstad exists: German ASW values that the English constants
// would push far below zero must land in a usable part of the scale.
func TestAmstadIsNotFlesch(t *testing.T) {
	const asl, asw = 16, 1.9 // unremarkable German prose
	de, err := Amstad(mkDoc(textproc.LangGerman, asl, asw))
	if err != nil {
		t.Fatalf("Amstad: %v", err)
	}
	en, err := Flesch(mkDoc(textproc.LangEnglish, asl, asw))
	if err != nil {
		t.Fatalf("Flesch: %v", err)
	}
	if de.Score <= 0 || de.Score >= 100 {
		t.Fatalf("amstad score %v is off the usable scale for ordinary German", de.Score)
	}
	if en.Score >= de.Score {
		t.Fatalf("flesch (%v) should be harsher than amstad (%v) on the same counts", en.Score, de.Score)
	}
}

func TestAmstadExtra(t *testing.T) {
	d := &textproc.Doc{
		Language: textproc.LangGerman,
		Stats: textproc.Stats{
			Sentences:           3,
			Words:               42,
			Syllables:           78,
			AvgSentenceLength:   14,
			AvgSyllablesPerWord: 1.857142857,
		},
	}
	got, err := Amstad(d)
	if err != nil {
		t.Fatalf("Amstad: %v", err)
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

func TestAmstadLanguageFallback(t *testing.T) {
	got, err := Amstad(mkDoc(textproc.LangAuto, 10, 1.6))
	if err != nil {
		t.Fatalf("Amstad: %v", err)
	}
	if got.Language != "de" {
		t.Fatalf("language = %q, want de", got.Language)
	}
}

func TestAmstadEmptyDoc(t *testing.T) {
	for name, d := range map[string]*textproc.Doc{
		"zero doc":     {},
		"no words":     {Stats: textproc.Stats{Sentences: 3}},
		"no sentences": {Stats: textproc.Stats{Words: 12}},
		"nil":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Amstad(d)
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
		})
	}
}
