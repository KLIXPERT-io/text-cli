package readability

import (
	"errors"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/analyze"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// mkGermanDoc builds a Doc from the four inputs the Wiener formulas read, as
// counts out of exactly 100 words in 10 sentences. With 100 words a count and a
// percentage are the same number, which keeps the arithmetic in the test
// comments checkable by eye.
//
//	poly → MS (% of words with 3+ syllables)
//	long → IW (% of words longer than 6 characters)
//	mono → ES (% of one-syllable words)
//	sl   → SL (words per sentence), set directly
func mkGermanDoc(sl float64, poly, long, mono int) *textproc.Doc {
	return &textproc.Doc{
		Language: textproc.LangGerman,
		Stats: textproc.Stats{
			Sentences:           10,
			Words:               100,
			Syllables:           180,
			Characters:          600,
			PolysyllabicWords:   poly,
			LongWords:           long,
			MonosyllabicWords:   mono,
			AvgSentenceLength:   sl,
			AvgSyllablesPerWord: 1.8,
			AvgWordLength:       6,
		},
	}
}

func TestWSTFFormulas(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*textproc.Doc) (analyze.Result, error)
		// doc inputs
		sl               float64
		poly, long, mono int
		// expectations
		metric string
		score  float64
		level  string
	}{
		// MS=20, SL=10, IW=30, ES=50.
		// WSTF1 = 0.1935·20 + 0.1672·10 + 0.1297·30 − 0.0327·50 − 0.875
		//       = 3.87 + 1.672 + 3.891 − 1.635 − 0.875 = 6.923 → 6.9
		{"wstf1 middling", WSTF1, 10, 20, 30, 50, "wstf1", 6.9, "leicht"},
		// WSTF2 = 0.2007·20 + 0.1682·10 + 0.1373·30 − 2.779
		//       = 4.014 + 1.682 + 4.119 − 2.779 = 7.036 → 7.0
		{"wstf2 middling", WSTF2, 10, 20, 30, 50, "wstf2", 7, "leicht"},
		// WSTF3 = 0.2963·20 + 0.1905·10 − 1.1144
		//       = 5.926 + 1.905 − 1.1144 = 6.7166 → 6.7
		{"wstf3 middling", WSTF3, 10, 20, 30, 50, "wstf3", 6.7, "leicht"},
		// WSTF4 = 0.2744·20 + 0.2656·10 − 1.693
		//       = 5.488 + 2.656 − 1.693 = 6.451 → 6.5
		{"wstf4 middling", WSTF4, 10, 20, 30, 50, "wstf4", 6.5, "leicht"},

		// MS=40, SL=25, IW=60, ES=20 — a dense academic paragraph.
		// WSTF1 = 7.74 + 4.18 + 7.782 − 0.654 − 0.875 = 18.173 → 18.2,
		// which is above the calibrated range, hence the out-of-scale label.
		{"wstf1 academic", WSTF1, 25, 40, 60, 20, "wstf1", 18.2, "über der Skala"},
		// WSTF2 = 0.2007·40 + 0.1682·25 + 0.1373·60 − 2.779
		//       = 8.028 + 4.205 + 8.238 − 2.779 = 17.692 → 17.7
		{"wstf2 academic", WSTF2, 25, 40, 60, 20, "wstf2", 17.7, "über der Skala"},
		// WSTF3 = 0.2963·40 + 0.1905·25 − 1.1144
		//       = 11.852 + 4.7625 − 1.1144 = 15.5001 → 15.5
		{"wstf3 academic", WSTF3, 25, 40, 60, 20, "wstf3", 15.5, "über der Skala"},
		// WSTF4 = 0.2744·40 + 0.2656·25 − 1.693
		//       = 10.976 + 6.64 − 1.693 = 15.923 → 15.9
		{"wstf4 academic", WSTF4, 25, 40, 60, 20, "wstf4", 15.9, "über der Skala"},

		// MS=0, SL=5, IW=0, ES=100 — a primer. Nothing clamps the score, so it
		// falls below the scale and is labelled as such rather than as a grade.
		// WSTF1 = 0 + 0.836 + 0 − 3.27 − 0.875 = −3.309 → −3.3
		{"wstf1 primer", WSTF1, 5, 0, 0, 100, "wstf1", -3.3, "unter der Skala"},

		// A text landing inside the scale: MS=10, SL=14, IW=25, ES=45.
		// WSTF1 = 1.935 + 2.3408 + 3.2425 − 1.4715 − 0.875 = 5.1718 → 5.2
		{"wstf1 in range", WSTF1, 14, 10, 25, 45, "wstf1", 5.2, "sehr leicht"},
		// MS=25, SL=18, IW=45, ES=35.
		// WSTF1 = 4.8375 + 3.0096 + 5.8365 − 1.1445 − 0.875 = 11.6641 → 11.7
		{"wstf1 hard", WSTF1, 18, 25, 45, 35, "wstf1", 11.7, "schwer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn(mkGermanDoc(tt.sl, tt.poly, tt.long, tt.mono))
			if err != nil {
				t.Fatalf("%s: %v", tt.metric, err)
			}
			if got.Score != tt.score {
				t.Fatalf("score = %v, want %v", got.Score, tt.score)
			}
			if got.Level != tt.level {
				t.Fatalf("level = %q, want %q", got.Level, tt.level)
			}
			if got.Metric != tt.metric {
				t.Fatalf("metric = %q, want %q", got.Metric, tt.metric)
			}
			if got.Language != "de" {
				t.Fatalf("language = %q, want de", got.Language)
			}
			// The scale string must state the inversion: a consumer reading a
			// WSTF next to a Flesch has to be told which way the number runs.
			if got.Scale != wstfScale {
				t.Fatalf("scale = %q, want %q", got.Scale, wstfScale)
			}
			if got.Grade == "" {
				t.Fatal("grade is empty")
			}
		})
	}
}

// Every band edge of the school-grade table. The boundaries are inclusive at the
// top — "≤6 ist sehr leicht" — which is the opposite convention to LIX, so it is
// worth pinning.
func TestWSTFBandEdges(t *testing.T) {
	edges := []struct {
		score float64
		level string
		grade string
	}{
		{4, "sehr leicht", "4.–6. Schulstufe"},
		{5.9, "sehr leicht", "4.–6. Schulstufe"},
		{6, "sehr leicht", "4.–6. Schulstufe"},
		{6.1, "leicht", "6.–8. Schulstufe"},
		{8, "leicht", "6.–8. Schulstufe"},
		{8.1, "mittel", "8.–10. Schulstufe"},
		{10, "mittel", "8.–10. Schulstufe"},
		{10.1, "schwer", "10.–12. Schulstufe"},
		{12, "schwer", "10.–12. Schulstufe"},
		{12.1, "sehr schwer", "Studium"},
		{15, "sehr schwer", "Studium"},
	}
	for _, e := range edges {
		level, grade := classifyGrade(wstfBands, e.score, true)
		if level != e.level || grade != e.grade {
			t.Fatalf("classifyGrade(%v) = %q/%q, want %q/%q", e.score, level, grade, e.level, e.grade)
		}
	}
}

// Outside 4–15 the formula has left the Schulstufe scale it was fitted to. The
// score is still reported raw — this package never clamps — but it must not be
// dressed up as a school grade.
func TestWSTFOutOfRangeIsLabelled(t *testing.T) {
	// MS=0, SL=5, IW=0, ES=100 → −3.3, well under the scale.
	below, err := WSTF1(mkGermanDoc(5, 0, 0, 100))
	if err != nil {
		t.Fatalf("WSTF1: %v", err)
	}
	if below.Score != -3.3 {
		t.Fatalf("score = %v, want the raw −3.3 (nothing may clamp it)", below.Score)
	}
	if below.Level != "unter der Skala" || below.Grade != "unter der 4. Schulstufe" {
		t.Fatalf("level/grade = %q/%q, want the out-of-scale labels", below.Level, below.Grade)
	}
	note, _ := below.Extra["note"].(string)
	if note == "" {
		t.Fatal("a score below the scale must carry a note in Extra")
	}

	// MS=40, SL=25, IW=60, ES=20 → 18.2, over the scale.
	above, err := WSTF1(mkGermanDoc(25, 40, 60, 20))
	if err != nil {
		t.Fatalf("WSTF1: %v", err)
	}
	if above.Level != "über der Skala" {
		t.Fatalf("level = %q, want the out-of-scale label", above.Level)
	}
	if _, ok := above.Extra["note"]; !ok {
		t.Fatal("a score above the scale must carry a note in Extra")
	}

	// In range, the note must be absent: it is a warning, not decoration.
	in, err := WSTF1(mkGermanDoc(14, 10, 25, 45))
	if err != nil {
		t.Fatalf("WSTF1: %v", err)
	}
	if _, ok := in.Extra["note"]; ok {
		t.Fatalf("in-range score %v should carry no note: %v", in.Score, in.Extra["note"])
	}
}

// Extra reports the inputs each variant actually used, and no others: WSTF3 and
// WSTF4 never look at long or monosyllabic words, so claiming them as evidence
// would be a lie.
func TestWSTFExtraReportsOnlyItsInputs(t *testing.T) {
	d := mkGermanDoc(10, 20, 30, 50)

	one, err := WSTF1(d)
	if err != nil {
		t.Fatalf("WSTF1: %v", err)
	}
	want := map[string]any{
		"ms":                 20.0,
		"sl":                 10.0,
		"iw":                 30.0,
		"es":                 50.0,
		"words":              100,
		"sentences":          10,
		"polysyllabic_words": 20,
		"long_words":         30,
		"monosyllabic_words": 50,
	}
	for k, v := range want {
		if one.Extra[k] != v {
			t.Fatalf("wstf1 extra[%q] = %v (%T), want %v (%T)", k, one.Extra[k], one.Extra[k], v, v)
		}
	}

	two, err := WSTF2(d)
	if err != nil {
		t.Fatalf("WSTF2: %v", err)
	}
	if _, ok := two.Extra["es"]; ok {
		t.Fatal("wstf2 does not use ES and must not report it")
	}
	if two.Extra["iw"] != 30.0 {
		t.Fatalf("wstf2 extra[iw] = %v, want 30", two.Extra["iw"])
	}

	for name, fn := range map[string]func(*textproc.Doc) (analyze.Result, error){"wstf3": WSTF3, "wstf4": WSTF4} {
		got, err := fn(d)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, k := range []string{"iw", "es", "long_words", "monosyllabic_words"} {
			if _, ok := got.Extra[k]; ok {
				t.Fatalf("%s does not use %s and must not report it", name, k)
			}
		}
		if got.Extra["ms"] != 20.0 || got.Extra["sl"] != 10.0 {
			t.Fatalf("%s extra = %v, want ms 20 and sl 10", name, got.Extra)
		}
	}
}

func TestWSTFEmptyDoc(t *testing.T) {
	fns := map[string]func(*textproc.Doc) (analyze.Result, error){
		"wstf1": WSTF1, "wstf2": WSTF2, "wstf3": WSTF3, "wstf4": WSTF4,
	}
	docs := map[string]*textproc.Doc{
		"zero doc":     {},
		"no words":     {Stats: textproc.Stats{Sentences: 3}},
		"no sentences": {Stats: textproc.Stats{Words: 12}},
		"nil":          nil,
	}
	for name, fn := range fns {
		for docName, d := range docs {
			t.Run(name+"/"+docName, func(t *testing.T) {
				_, err := fn(d)
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
}

// All four variants are registered, `wstf` is the alias for the general-purpose
// one, and none of them leaks into English.
func TestWSTFRegistered(t *testing.T) {
	for _, lookup := range []string{"wstf", "wstf1", "wiener-sachtextformel", "WSTF"} {
		m, ok := analyze.Get(lookup)
		if !ok {
			t.Fatalf("analyze.Get(%q) not found", lookup)
		}
		if m.Name != "wstf1" {
			t.Fatalf("%q resolved to %q, want wstf1", lookup, m.Name)
		}
	}
	for _, name := range []string{"wstf1", "wstf2", "wstf3", "wstf4"} {
		m, ok := analyze.Get(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if !m.Supports(textproc.LangGerman) {
			t.Fatalf("%s should support de", name)
		}
		if m.Supports(textproc.LangEnglish) {
			t.Fatalf("%s must not claim en: the coefficients are fitted on German", name)
		}
		if m.Compute == nil || m.Description == "" || m.Title == "" {
			t.Fatalf("%s is incompletely registered: %+v", name, m)
		}
	}
}

// The direction lookup is what the CLI's --fail-under/--fail-over gate reads;
// getting it wrong inverts a CI verdict, so it is asserted for every metric.
func TestDirectionOf(t *testing.T) {
	tests := []struct {
		name string
		want Direction
	}{
		{"flesch", HigherIsEasier},
		{"fre", HigherIsEasier}, // alias
		{"amstad", HigherIsEasier},
		{"fre-de", HigherIsEasier}, // alias
		{"wstf1", LowerIsEasier},
		{"wstf", LowerIsEasier}, // alias
		{"wstf2", LowerIsEasier},
		{"wstf3", LowerIsEasier},
		{"wstf4", LowerIsEasier},
		{"lix", LowerIsEasier},
		{"flesch-kincaid", LowerIsEasier},
		{"fkgl", LowerIsEasier}, // alias
		{"gunning-fog", LowerIsEasier},
		{"fog", LowerIsEasier}, // alias
		{"smog", LowerIsEasier},
		{"coleman-liau", LowerIsEasier},
		{"cli", LowerIsEasier}, // alias
		{"ari", LowerIsEasier},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, known := DirectionOf(tt.name)
			if !known {
				t.Fatalf("DirectionOf(%q) is unknown; the CI gate would skip it", tt.name)
			}
			if got != tt.want {
				t.Fatalf("DirectionOf(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}

	// Every registered metric must declare a direction, or a gate silently
	// ignores it. This is the check that fails when someone adds a metric and
	// forgets the table in wstf.go.
	for _, m := range analyze.All() {
		if _, known := DirectionOf(m.Name); !known {
			t.Fatalf("metric %q declares no direction: add it to the directions table", m.Name)
		}
	}

	if _, known := DirectionOf("no-such-metric"); known {
		t.Fatal("an unregistered name must report an unknown direction, not a default")
	}
}
