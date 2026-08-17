package readability

import (
	"errors"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/analyze"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// mkEnglishDoc builds a Doc with every count the grade-level pack reads set by
// hand. The defaults are 100 words in 10 sentences of 500 letters — ASL 10,
// ASW 1.5, 5 letters per word — so each formula's arithmetic can be checked by
// eye in the comments below.
func mkEnglishDoc(sentences, words, syllables, characters, poly int) *textproc.Doc {
	d := &textproc.Doc{
		Language: textproc.LangEnglish,
		Stats: textproc.Stats{
			Sentences:         sentences,
			Words:             words,
			Syllables:         syllables,
			Characters:        characters,
			PolysyllabicWords: poly,
		},
	}
	if sentences > 0 {
		d.Stats.AvgSentenceLength = float64(words) / float64(sentences)
	}
	if words > 0 {
		d.Stats.AvgSyllablesPerWord = float64(syllables) / float64(words)
		d.Stats.AvgWordLength = float64(characters) / float64(words)
	}
	return d
}

// The reference document: 10 sentences, 100 words, 150 syllables, 500 letters,
// 20 polysyllabic words. ASL 10, ASW 1.5, 5 letters/word, MS 20%.
func mkRefDoc() *textproc.Doc { return mkEnglishDoc(10, 100, 150, 500, 20) }

func TestEnglishGradeFormulas(t *testing.T) {
	tests := []struct {
		name   string
		fn     func(*textproc.Doc) (analyze.Result, error)
		metric string
		title  string
		score  float64
		level  string
		grade  string
	}{
		// FK = 0.39·10 + 11.8·1.5 − 15.59 = 3.9 + 17.7 − 15.59 = 6.01 → 6.0
		{"flesch-kincaid", FleschKincaid, "flesch-kincaid", "Flesch-Kincaid Grade Level",
			6, "very easy", "elementary school (up to 6th grade)"},
		// Fog = 0.4·(10 + 20) = 12.0
		{"gunning-fog", GunningFog, "gunning-fog", "Gunning Fog Index",
			12, "medium", "high school (10th–12th grade)"},
		// SMOG = 1.0430·sqrt(20·30/10) + 3.1291 = 1.0430·sqrt(60) + 3.1291
		//      = 1.0430·7.745967 + 3.1291 = 8.079043 + 3.1291 = 11.208143 → 11.2
		{"smog", SMOG, "smog", "SMOG Index",
			11.2, "medium", "high school (10th–12th grade)"},
		// CLI: L = 5·100 = 500 letters per 100 words, S = 10 sentences per 100
		// words. 0.0588·500 − 0.296·10 − 15.8 = 29.4 − 2.96 − 15.8 = 10.64 → 10.6
		{"coleman-liau", ColemanLiau, "coleman-liau", "Coleman-Liau Index",
			10.6, "medium", "high school (10th–12th grade)"},
		// ARI = 4.71·5 + 0.5·10 − 21.43 = 23.55 + 5 − 21.43 = 7.12 → 7.1
		{"ari", ARI, "ari", "Automated Readability Index",
			7.1, "easy", "middle school (7th–9th grade)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn(mkRefDoc())
			if err != nil {
				t.Fatalf("%s: %v", tt.metric, err)
			}
			if got.Score != tt.score {
				t.Fatalf("score = %v, want %v", got.Score, tt.score)
			}
			if got.Level != tt.level || got.Grade != tt.grade {
				t.Fatalf("level/grade = %q/%q, want %q/%q", got.Level, got.Grade, tt.level, tt.grade)
			}
			if got.Metric != tt.metric || got.Title != tt.title {
				t.Fatalf("metric/title = %q/%q, want %q/%q", got.Metric, got.Title, tt.metric, tt.title)
			}
			if got.Language != "en" {
				t.Fatalf("language = %q, want en", got.Language)
			}
			// Every one of these is a grade level, and the scale string has to
			// say so: read as a reading-ease score, 12 would look excellent.
			if got.Scale != gradeScale {
				t.Fatalf("scale = %q, want %q", got.Scale, gradeScale)
			}
			if !strings.Contains(got.Scale, "lower is easier") {
				t.Fatalf("scale %q must state the direction", got.Scale)
			}
		})
	}
}

// A harder document moves every formula the same way — up.
func TestEnglishGradeHarderText(t *testing.T) {
	// 5 sentences, 100 words, 200 syllables, 700 letters, 40 polysyllabic.
	// ASL 20, ASW 2.0, 7 letters/word, MS 40%.
	d := mkEnglishDoc(5, 100, 200, 700, 40)
	tests := []struct {
		name  string
		fn    func(*textproc.Doc) (analyze.Result, error)
		score float64
		level string
	}{
		// FK = 0.39·20 + 11.8·2 − 15.59 = 7.8 + 23.6 − 15.59 = 15.81 → 15.8
		{"flesch-kincaid", FleschKincaid, 15.8, "difficult"},
		// Fog = 0.4·(20 + 40) = 24.0
		{"gunning-fog", GunningFog, 24, "very difficult"},
		// SMOG = 1.0430·sqrt(40·30/5) + 3.1291 = 1.0430·sqrt(240) + 3.1291
		//      = 1.0430·15.491933 + 3.1291 = 16.158086 + 3.1291 = 19.287186 → 19.3
		{"smog", SMOG, 19.3, "very difficult"},
		// CLI: L = 700, S = 5. 0.0588·700 − 0.296·5 − 15.8
		//    = 41.16 − 1.48 − 15.8 = 23.88 → 23.9
		{"coleman-liau", ColemanLiau, 23.9, "very difficult"},
		// ARI = 4.71·7 + 0.5·20 − 21.43 = 32.97 + 10 − 21.43 = 21.54 → 21.5
		{"ari", ARI, 21.5, "very difficult"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn(d)
			if err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			if got.Score != tt.score {
				t.Fatalf("score = %v, want %v", got.Score, tt.score)
			}
			if got.Level != tt.level {
				t.Fatalf("level = %q, want %q", got.Level, tt.level)
			}
		})
	}
}

// Every band edge of the shared US grade table, exercised through ARI by solving
// its sentence-length term for the boundary. The boundaries are inclusive at the
// top: grade 12.0 is still high school.
func TestUSGradeBandEdges(t *testing.T) {
	edges := []struct {
		score float64
		level string
		grade string
	}{
		{0, "very easy", "elementary school (up to 6th grade)"},
		{5.9, "very easy", "elementary school (up to 6th grade)"},
		{6, "very easy", "elementary school (up to 6th grade)"},
		{6.1, "easy", "middle school (7th–9th grade)"},
		{9, "easy", "middle school (7th–9th grade)"},
		{9.1, "medium", "high school (10th–12th grade)"},
		{12, "medium", "high school (10th–12th grade)"},
		{12.1, "difficult", "college"},
		{16, "difficult", "college"},
		{16.1, "very difficult", "college graduate"},
		{40, "very difficult", "college graduate"},
		// Nothing clamps: a negative grade still gets the easiest label.
		{-3.4, "very easy", "elementary school (up to 6th grade)"},
	}
	for _, e := range edges {
		level, grade := classifyGrade(usGradeBands, e.score, true)
		if level != e.level || grade != e.grade {
			t.Fatalf("classifyGrade(%v) = %q/%q, want %q/%q", e.score, level, grade, e.level, e.grade)
		}
	}
}

// SMOG is calibrated on 30-sentence samples. On a shorter document it still
// scores — a batch needs a number for every row — but it says so.
func TestSMOGShortSampleNote(t *testing.T) {
	short, err := SMOG(mkRefDoc()) // 10 sentences
	if err != nil {
		t.Fatalf("SMOG: %v", err)
	}
	note, _ := short.Extra["note"].(string)
	if note == "" {
		t.Fatal("a document under 30 sentences must carry a note in Extra")
	}
	if !strings.Contains(note, "30") {
		t.Fatalf("note should name the 30-sentence minimum, got %q", note)
	}
	if short.Score == 0 {
		t.Fatal("a short document must still be scored, not refused")
	}

	// 30 sentences, 600 words, 60 polysyllabic:
	// SMOG = 1.0430·sqrt(60·30/30) + 3.1291 = 1.0430·sqrt(60) + 3.1291 = 11.2
	long, err := SMOG(mkEnglishDoc(30, 600, 900, 3000, 60))
	if err != nil {
		t.Fatalf("SMOG: %v", err)
	}
	if _, ok := long.Extra["note"]; ok {
		t.Fatalf("a 30-sentence document needs no caveat: %v", long.Extra["note"])
	}
	if long.Score != 11.2 {
		t.Fatalf("score = %v, want 11.2", long.Score)
	}
}

func TestEnglishGradeExtra(t *testing.T) {
	d := mkRefDoc()
	tests := []struct {
		name string
		fn   func(*textproc.Doc) (analyze.Result, error)
		want map[string]any
	}{
		{"flesch-kincaid", FleschKincaid, map[string]any{
			"asl": 10.0, "asw": 1.5, "words": 100, "sentences": 10, "syllables": 150,
		}},
		{"gunning-fog", GunningFog, map[string]any{
			"asl": 10.0, "ms": 20.0, "polysyllabic_words": 20, "words": 100, "sentences": 10,
		}},
		{"smog", SMOG, map[string]any{
			"polysyllabic_words": 20, "words": 100, "sentences": 10,
		}},
		{"coleman-liau", ColemanLiau, map[string]any{
			"l": 500.0, "s": 10.0, "characters": 500, "words": 100, "sentences": 10,
		}},
		{"ari", ARI, map[string]any{
			"asl": 10.0, "chars_per_word": 5.0, "characters": 500, "words": 100, "sentences": 10,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn(d)
			if err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			for k, v := range tt.want {
				if got.Extra[k] != v {
					t.Fatalf("extra[%q] = %v (%T), want %v (%T)", k, got.Extra[k], got.Extra[k], v, v)
				}
			}
		})
	}
}

// A Doc carrying only raw counts must score identically to one carrying the
// precomputed averages: the fallbacks in asl/asw/awl are what make a hand-built
// Doc (or a future caller that fills only counts) safe.
func TestEnglishGradeFallsBackToRawCounts(t *testing.T) {
	raw := &textproc.Doc{
		Language: textproc.LangEnglish,
		Stats: textproc.Stats{
			Sentences: 10, Words: 100, Syllables: 150, Characters: 500, PolysyllabicWords: 20,
		},
	}
	for name, fn := range map[string]func(*textproc.Doc) (analyze.Result, error){
		"flesch-kincaid": FleschKincaid,
		"gunning-fog":    GunningFog,
		"smog":           SMOG,
		"coleman-liau":   ColemanLiau,
		"ari":            ARI,
	} {
		withAvgs, err := fn(mkRefDoc())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		fromCounts, err := fn(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if withAvgs.Score != fromCounts.Score {
			t.Fatalf("%s: %v from counts, %v from averages", name, fromCounts.Score, withAvgs.Score)
		}
	}
}

func TestEnglishGradeEmptyDoc(t *testing.T) {
	fns := map[string]func(*textproc.Doc) (analyze.Result, error){
		"flesch-kincaid": FleschKincaid,
		"gunning-fog":    GunningFog,
		"smog":           SMOG,
		"coleman-liau":   ColemanLiau,
		"ari":            ARI,
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
				if errs.ExitCode(err) != 5 {
					t.Fatalf("exit code = %d, want 5", errs.ExitCode(err))
				}
			})
		}
	}
}

func TestEnglishGradeRegistered(t *testing.T) {
	tests := []struct {
		lookup string
		want   string
	}{
		{"flesch-kincaid", "flesch-kincaid"},
		{"fkgl", "flesch-kincaid"},
		{"gunning-fog", "gunning-fog"},
		{"fog", "gunning-fog"},
		{"smog", "smog"},
		{"coleman-liau", "coleman-liau"},
		{"cli", "coleman-liau"},
		{"ari", "ari"},
		{"ARI", "ari"},
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
			if !m.Supports(textproc.LangEnglish) {
				t.Fatalf("%s should support en", m.Name)
			}
			if m.Supports(textproc.LangGerman) {
				t.Fatalf("%s must not claim de: the constants are fitted on English", m.Name)
			}
			if m.Description == "" || m.Compute == nil {
				t.Fatalf("%s is incompletely registered: %+v", m.Name, m)
			}
			// `text metrics list` is where the direction is discovered.
			if !strings.Contains(m.Description, "LOWER is easier") {
				t.Fatalf("%s description must state the direction: %q", m.Name, m.Description)
			}
		})
	}
}
