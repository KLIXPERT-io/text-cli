package readability

import (
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/analyze"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// mkDoc builds a Doc with the averages set directly. The formulas are tested
// against these numbers rather than against tokenizer output, so a change in
// tokenization can never quietly change what "the Flesch formula" means here.
func mkDoc(lang textproc.Language, asl, asw float64) *textproc.Doc {
	return &textproc.Doc{
		Language: lang,
		Stats: textproc.Stats{
			Sentences:           10,
			Words:               100,
			Syllables:           150,
			AvgSentenceLength:   asl,
			AvgSyllablesPerWord: asw,
		},
	}
}

func TestClassifyBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		bands []band
		score float64
		level string
		grade string
	}{
		// Flesch: every band edge is inclusive at the bottom.
		{"flesch top", fleschBands, 100, "very easy", "5th grade"},
		{"flesch 90", fleschBands, 90, "very easy", "5th grade"},
		{"flesch just under 90", fleschBands, 89.9, "easy", "6th grade"},
		{"flesch 80", fleschBands, 80, "easy", "6th grade"},
		{"flesch just under 80", fleschBands, 79.9, "fairly easy", "7th grade"},
		{"flesch 70", fleschBands, 70, "fairly easy", "7th grade"},
		{"flesch just under 70", fleschBands, 69.9, "standard", "8th–9th grade"},
		{"flesch 60", fleschBands, 60, "standard", "8th–9th grade"},
		{"flesch just under 60", fleschBands, 59.9, "fairly difficult", "10th–12th grade"},
		{"flesch 50", fleschBands, 50, "fairly difficult", "10th–12th grade"},
		{"flesch just under 50", fleschBands, 49.9, "difficult", "college"},
		{"flesch 30", fleschBands, 30, "difficult", "college"},
		{"flesch just under 30", fleschBands, 29.9, "very confusing", "college graduate"},
		{"flesch 0", fleschBands, 0, "very confusing", "college graduate"},
		// Only the lookup clamps, so out-of-range scores still get a label.
		{"flesch negative clamps low", fleschBands, -42.5, "very confusing", "college graduate"},
		{"flesch above 100 clamps high", fleschBands, 121.2, "very easy", "5th grade"},

		{"amstad top", amstadBands, 100, "sehr leicht", "4th grade"},
		{"amstad 90", amstadBands, 90, "sehr leicht", "4th grade"},
		{"amstad just under 90", amstadBands, 89.9, "leicht", "5th–6th grade"},
		{"amstad 80", amstadBands, 80, "leicht", "5th–6th grade"},
		{"amstad just under 80", amstadBands, 79.9, "mittelleicht", "7th grade"},
		{"amstad 70", amstadBands, 70, "mittelleicht", "7th grade"},
		{"amstad just under 70", amstadBands, 69.9, "mittel", "8th–9th grade"},
		{"amstad 60", amstadBands, 60, "mittel", "8th–9th grade"},
		{"amstad just under 60", amstadBands, 59.9, "mittelschwer", "10th–12th grade"},
		{"amstad 50", amstadBands, 50, "mittelschwer", "10th–12th grade"},
		{"amstad just under 50", amstadBands, 49.9, "schwer", "Studium"},
		{"amstad 30", amstadBands, 30, "schwer", "Studium"},
		{"amstad just under 30", amstadBands, 29.9, "sehr schwer", "akademisch"},
		{"amstad 0", amstadBands, 0, "sehr schwer", "akademisch"},
		{"amstad negative clamps low", amstadBands, -55.5, "sehr schwer", "akademisch"},
		{"amstad above 100 clamps high", amstadBands, 116.5, "sehr leicht", "4th grade"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, grade := classify(tt.bands, tt.score)
			if level != tt.level || grade != tt.grade {
				t.Fatalf("classify(%v) = %q/%q, want %q/%q", tt.score, level, grade, tt.level, tt.grade)
			}
		})
	}
}

func TestRound(t *testing.T) {
	tests := []struct {
		in     float64
		places int
		want   float64
	}{
		{69.785, 1, 69.8},
		{-97.715, 1, -97.7},
		{1.8571428, 2, 1.86},
		{14, 2, 14},
	}
	for _, tt := range tests {
		if got := round(tt.in, tt.places); got != tt.want {
			t.Fatalf("round(%v, %d) = %v, want %v", tt.in, tt.places, got, tt.want)
		}
	}
}

// The averages are the metric's real inputs, but a Doc built from raw counts
// alone must still score rather than silently read zero.
func TestAveragesFallBackToCounts(t *testing.T) {
	d := &textproc.Doc{
		Language: textproc.LangEnglish,
		Stats:    textproc.Stats{Sentences: 2, Words: 20, Syllables: 30},
	}
	if got := asl(d); got != 10 {
		t.Fatalf("asl = %v, want 10", got)
	}
	if got := asw(d); got != 1.5 {
		t.Fatalf("asw = %v, want 1.5", got)
	}
}

func TestRegistered(t *testing.T) {
	tests := []struct {
		lookup    string
		want      string
		supports  textproc.Language
		rejects   textproc.Language
		wantTitle string
	}{
		{"flesch", "flesch", textproc.LangEnglish, textproc.LangGerman, "Flesch Reading Ease"},
		{"FRE", "flesch", textproc.LangEnglish, textproc.LangGerman, "Flesch Reading Ease"},
		{"flesch-reading-ease", "flesch", textproc.LangEnglish, textproc.LangGerman, "Flesch Reading Ease"},
		{"amstad", "amstad", textproc.LangGerman, textproc.LangEnglish, "Amstad (Flesch für Deutsch)"},
		{"flesch-de", "amstad", textproc.LangGerman, textproc.LangEnglish, "Amstad (Flesch für Deutsch)"},
		{"flesch-amstad", "amstad", textproc.LangGerman, textproc.LangEnglish, "Amstad (Flesch für Deutsch)"},
		{"fre-de", "amstad", textproc.LangGerman, textproc.LangEnglish, "Amstad (Flesch für Deutsch)"},
	}
	for _, tt := range tests {
		t.Run(tt.lookup, func(t *testing.T) {
			m, ok := analyze.Get(tt.lookup)
			if !ok {
				t.Fatalf("analyze.Get(%q) not found", tt.lookup)
			}
			if m.Name != tt.want {
				t.Fatalf("resolved to %q, want %q", m.Name, tt.want)
			}
			if m.Title != tt.wantTitle {
				t.Fatalf("title = %q, want %q", m.Title, tt.wantTitle)
			}
			if !m.Supports(tt.supports) {
				t.Fatalf("%s should support %s", m.Name, tt.supports)
			}
			if m.Supports(tt.rejects) {
				t.Fatalf("%s should not support %s", m.Name, tt.rejects)
			}
			if m.Description == "" {
				t.Fatalf("%s has no description; `text metrics list` would show a blank cell", m.Name)
			}
			if m.Compute == nil {
				t.Fatalf("%s has no Compute", m.Name)
			}
		})
	}
}

// --metrics auto resolves through the registry, so a language must map to
// exactly the formula calibrated for it.
func TestForLanguage(t *testing.T) {
	for lang, want := range map[textproc.Language]string{
		textproc.LangEnglish: "flesch",
		textproc.LangGerman:  "amstad",
	} {
		got := analyze.ForLanguage(lang)
		found := false
		for _, m := range got {
			if m.Name == want {
				found = true
			}
			if !m.Supports(lang) {
				t.Fatalf("ForLanguage(%s) returned %s which does not support it", lang, m.Name)
			}
		}
		if !found {
			t.Fatalf("ForLanguage(%s) did not include %s", lang, want)
		}
	}
	if got := analyze.ForLanguage(textproc.Language("fr")); len(got) != 0 {
		t.Fatalf("ForLanguage(fr) = %v, want none", got)
	}
}
