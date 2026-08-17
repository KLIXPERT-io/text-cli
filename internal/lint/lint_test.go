package lint

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// A realistic paragraph of German administrative prose. Every German rule in
// this package has something to say about it, which is the point: this is what
// the tool is for.
const bureaucraticDE = `Im Rahmen der Durchführung des Projektes wird seitens der Abteilung eine ` +
	`umfassende Überprüfung der bestehenden Prozesse vorgenommen, damit die Einhaltung der ` +
	`gesetzlichen Anforderungen sichergestellt werden kann. Hinsichtlich der Inanspruchnahme ` +
	`externer Dienstleister wurde eine Entscheidung noch nicht getroffen. Die Bearbeitung der ` +
	`Anträge erfolgt unter Berücksichtigung der Vorgaben. Die Umsetzung der Maßnahmen könnte ` +
	`gegebenenfalls verschoben werden, sofern die Genehmigung nicht rechtzeitig erteilt wird.`

func mustDoc(t *testing.T, text string, lang textproc.Language) *textproc.Doc {
	t.Helper()
	d, err := textproc.Analyze(text, lang)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return d
}

// runRule runs exactly one rule and checks the offset invariant on everything
// it returns, so no test can pass with an excerpt that does not match its span.
func runRule(t *testing.T, text string, lang textproc.Language, name string, cfg Config) []Finding {
	t.Helper()
	d := mustDoc(t, text, lang)
	r, known, supported := GetFor(name, d.Language)
	if !known {
		t.Fatalf("rule %q is not registered", name)
	}
	if !supported {
		t.Fatalf("rule %q does not support language %q", name, d.Language)
	}
	fs := Run(d, []Rule{r}, cfg)
	assertOffsets(t, d, fs)
	return fs
}

func assertOffsets(t *testing.T, d *textproc.Doc, fs []Finding) {
	t.Helper()
	for _, f := range fs {
		if f.Start < 0 || f.End > len(d.Text) || f.End < f.Start {
			t.Fatalf("%s: span [%d:%d] is outside the document (%d bytes)", f.Rule, f.Start, f.End, len(d.Text))
		}
		if got := d.Text[f.Start:f.End]; got != f.Excerpt {
			t.Fatalf("%s: text[%d:%d] = %q, but excerpt = %q", f.Rule, f.Start, f.End, got, f.Excerpt)
		}
	}
}

// span is the expected byte range of a finding, located by its text rather than
// hand-counted, so the assertion stays exact and stays readable.
func span(t *testing.T, text, excerpt string) (int, int) {
	t.Helper()
	i := strings.Index(text, excerpt)
	if i < 0 {
		t.Fatalf("test bug: %q does not occur in the document", excerpt)
	}
	return i, i + len(excerpt)
}

func excerpts(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Excerpt)
	}
	return out
}

// nth returns the byte offset of the n-th (1-based) occurrence of sub.
func nth(t *testing.T, text, sub string, n int) int {
	t.Helper()
	off := 0
	for i := 0; i < n; i++ {
		j := strings.Index(text[off:], sub)
		if j < 0 {
			t.Fatalf("test bug: %q occurs fewer than %d times", sub, n)
		}
		off += j
		if i < n-1 {
			off += len(sub)
		}
	}
	return off
}

// assertExcerpts is the workhorse of the table tests: it asserts what a rule
// found, in order, by the exact source span of each finding. starts may pin the
// expected byte offsets; when it is nil the first occurrence of each excerpt is
// expected, which is what all but the repetition rules mean.
func assertExcerpts(t *testing.T, text string, fs []Finding, want []string, starts []int) {
	t.Helper()
	got := excerpts(fs)
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("findings = %#v, want %#v", got, want)
	}
	for i, w := range want {
		start, end := span(t, text, w)
		if starts != nil {
			start, end = starts[i], starts[i]+len(w)
		}
		if fs[i].Start != start || fs[i].End != end {
			t.Fatalf("finding %d (%s) spans [%d:%d], want [%d:%d]", i, fs[i].Rule, fs[i].Start, fs[i].End, start, end)
		}
	}
}

// ---------------------------------------------------------------------------
// The offset contract
// ---------------------------------------------------------------------------

// Byte offsets, not rune offsets. A German document is the test that matters:
// every umlaut and every ß makes the two disagree, and a caller slicing the
// source with rune offsets would cut a character in half.
func TestOffsetsAreBytesIntoTheSourceDocument(t *testing.T) {
	text := bureaucraticDE + ` Die Straßenverkehrsordnungsnovelle wird eigentlich sehr wahrscheinlich ` +
		`natürlich erst später wirksam.`
	d := mustDoc(t, text, textproc.LangGerman)
	fs := Run(d, ForLanguage(textproc.LangGerman), Config{})
	if len(fs) < 10 {
		t.Fatalf("expected a work list, got %d findings", len(fs))
	}
	assertOffsets(t, d, fs)

	// And prove the test could fail: at least one finding must sit far enough
	// into the text that its byte offset differs from its rune offset.
	divergent := false
	for _, f := range fs {
		if f.Start > 0 && utf8.RuneCountInString(text[:f.Start]) != f.Start {
			divergent = true
			break
		}
	}
	if !divergent {
		t.Fatal("no finding sits behind a multi-byte character; the invariant is untested")
	}

	// A caller must be able to apply an edit through the offsets alone.
	var long Finding
	for _, f := range fs {
		if f.Rule == "long-word" {
			long = f
		}
	}
	if long.Excerpt != "Straßenverkehrsordnungsnovelle" {
		t.Fatalf("long-word excerpt = %q", long.Excerpt)
	}
	edited := text[:long.Start] + "Novelle der StVO" + text[long.End:]
	if !strings.Contains(edited, "Die Novelle der StVO wird") {
		t.Fatalf("editing through the offsets produced:\n%s", edited)
	}
}

// The whole document, in German, with every applicable rule: the headline
// promise is that passive, nominalization and bureaucratic all fire on prose
// that looks exactly like this.
func TestGermanBureaucraticParagraph(t *testing.T) {
	d := mustDoc(t, bureaucraticDE, textproc.LangGerman)
	fs := Run(d, ForLanguage(textproc.LangGerman), Config{})
	assertOffsets(t, d, fs)

	sum := Summary(fs)
	for _, rule := range []string{"passive", "nominalization", "bureaucratic", "long-sentence", "hard-sentence", "filler"} {
		if sum[rule] == 0 {
			t.Fatalf("rule %q found nothing in a paragraph of Behördendeutsch: %v", rule, sum)
		}
	}
	if sum["passive"] < 4 {
		t.Fatalf("passive = %d, want the four passive constructions in the paragraph", sum["passive"])
	}
	// Messages are in the document's language, because that is where they will
	// be read.
	for _, f := range fs {
		if f.Rule == "bureaucratic" && !strings.Contains(f.Message, "Behördendeutsch") {
			t.Fatalf("German finding carries an English message: %q", f.Message)
		}
		if f.Suggestion == "" && f.Rule != "sentence-length-variance" {
			t.Fatalf("%s has no suggestion; a work list needs the fix, not just the complaint", f.Rule)
		}
	}
}

// ---------------------------------------------------------------------------
// Ordering and determinism
// ---------------------------------------------------------------------------

func TestFindingsAreSortedAndDeterministic(t *testing.T) {
	d := mustDoc(t, bureaucraticDE, textproc.LangGerman)
	rules := ForLanguage(textproc.LangGerman)
	first := Run(d, rules, Config{})

	for i := 1; i < len(first); i++ {
		a, b := first[i-1], first[i]
		if a.Start > b.Start || (a.Start == b.Start && a.Rule > b.Rule) {
			t.Fatalf("findings are out of order at %d: %s@%d then %s@%d", i, a.Rule, a.Start, b.Rule, b.Start)
		}
	}

	// Same input, reversed rule order: identical output. Anything else makes
	// the CSV diff in a CI job meaningless.
	reversed := make([]Rule, len(rules))
	for i, r := range rules {
		reversed[len(rules)-1-i] = r
	}
	second := Run(d, reversed, Config{})
	if !reflect.DeepEqual(first, second) {
		t.Fatal("findings depend on the order the rules ran in")
	}
	for i := 0; i < 3; i++ {
		if again := Run(d, rules, Config{}); !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d differs from run 0", i+2)
		}
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func TestRegistryResolvesPerLanguageVariants(t *testing.T) {
	de, known, supported := GetFor("passive", textproc.LangGerman)
	if !known || !supported || de.Title != "Passiv" {
		t.Fatalf("GetFor(passive, de) = %+v (known=%v supported=%v)", de, known, supported)
	}
	en, known, supported := GetFor("passive", textproc.LangEnglish)
	if !known || !supported || en.Title != "Passive voice" {
		t.Fatalf("GetFor(passive, en) = %+v", en)
	}
	if _, known, _ := GetFor("nope", textproc.LangGerman); known {
		t.Fatal("an unregistered rule reported as known")
	}
	// A German-only rule asked for in English: known, but not supported.
	if _, known, supported := GetFor("bureaucratic", textproc.LangEnglish); !known || supported {
		t.Fatalf("bureaucratic on en: known=%v supported=%v", known, supported)
	}
}

func TestRegistryListings(t *testing.T) {
	all := All()
	if len(all) < 11 {
		t.Fatalf("All() returned %d rules", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Name > all[i].Name {
			t.Fatalf("All() is not sorted: %q before %q", all[i-1].Name, all[i].Name)
		}
	}
	for _, r := range all {
		if r.Title == "" || r.Description == "" || len(r.Languages) == 0 || r.Severity == "" {
			t.Fatalf("incomplete discovery record: %+v", r)
		}
	}
	// Every language-agnostic rule must appear for both languages.
	deNames := map[string]bool{}
	for _, r := range ForLanguage(textproc.LangGerman) {
		deNames[r.Name] = true
	}
	for _, want := range []string{"long-sentence", "hard-sentence", "sentence-length-variance",
		"repeated-word", "repeated-sentence-start", "long-word", "passive", "nominalization",
		"filler", "modal-hedge", "bureaucratic"} {
		if !deNames[want] {
			t.Fatalf("rule %q is missing from ForLanguage(de)", want)
		}
	}
	if deNames["adverb"] || deNames["hedge"] {
		t.Fatalf("English-only rules leaked into ForLanguage(de): %v", deNames)
	}
	if got := Names(); len(got) == 0 || got[0] > got[len(got)-1] {
		t.Fatalf("Names() = %v", got)
	}
}

// Registering the same name twice for the same language is a programming error
// and must fail loudly at init, not silently shadow a rule.
func TestRegisterRejectsOverlappingLanguages(t *testing.T) {
	noop := func(*textproc.Doc, Config) []Finding { return nil }
	Register(Rule{
		Name: "zz-registry-test", Title: "t", Description: "d",
		Languages: []string{string(textproc.LangGerman)}, Check: noop,
	})
	// A second language for the same name is fine.
	Register(Rule{
		Name: "zz-registry-test", Title: "t", Description: "d",
		Languages: []string{string(textproc.LangEnglish)}, Check: noop,
	})
	if got := Variants("zz-registry-test"); len(got) != 2 {
		t.Fatalf("variants = %d, want 2", len(got))
	}

	defer func() {
		if recover() == nil {
			t.Fatal("registering a duplicate for the same language did not panic")
		}
	}()
	Register(Rule{
		Name: "zz-registry-test", Title: "t", Description: "d",
		Languages: []string{string(textproc.LangGerman)}, Check: noop,
	})
}

// ---------------------------------------------------------------------------
// Config, severity, helpers
// ---------------------------------------------------------------------------

func TestConfigDefaults(t *testing.T) {
	if got := (Config{}).WithDefaults(); !reflect.DeepEqual(got, Defaults()) {
		t.Fatalf("zero config did not fill in the defaults:\n%+v", got)
	}
	// A partial override keeps everything else.
	c := Config{MaxSentenceWords: 12}.WithDefaults()
	if c.MaxSentenceWords != 12 || c.MaxWordChars != Defaults().MaxWordChars {
		t.Fatalf("partial override = %+v", c)
	}
	// Raising the limit past the warn threshold must not make every long
	// sentence a warning.
	c = Config{MaxSentenceWords: 60}.WithDefaults()
	if c.WarnSentenceWords < c.MaxSentenceWords {
		t.Fatalf("warn threshold %d is below the limit %d", c.WarnSentenceWords, c.MaxSentenceWords)
	}
}

func TestSeverity(t *testing.T) {
	if !SeverityWarn.AtLeast(SeverityInfo) || SeverityInfo.AtLeast(SeverityWarn) {
		t.Fatal("severity ordering is wrong")
	}
	if s, ok := ParseSeverity(" WARN "); !ok || s != SeverityWarn {
		t.Fatalf("ParseSeverity(WARN) = %q, %v", s, ok)
	}
	if _, ok := ParseSeverity("fatal"); ok {
		t.Fatal("ParseSeverity accepted an unknown level")
	}
	fs := []Finding{{Rule: "a", Severity: SeverityInfo}, {Rule: "b", Severity: SeverityWarn}}
	if got := FilterSeverity(fs, SeverityWarn); len(got) != 1 || got[0].Rule != "b" {
		t.Fatalf("FilterSeverity = %+v", got)
	}
	if got := FilterSeverity(fs, SeverityInfo); len(got) != 2 {
		t.Fatalf("FilterSeverity(info) dropped findings: %+v", got)
	}
}

func TestShortenCountsRunes(t *testing.T) {
	if got := Shorten("Größenordnung", 40); got != "Größenordnung" {
		t.Fatalf("Shorten = %q", got)
	}
	if got := Shorten("Die  Prüfung\nläuft", 40); got != "Die Prüfung läuft" {
		t.Fatalf("Shorten did not collapse whitespace: %q", got)
	}
	got := Shorten("Größenordnungsbetrachtung", 10)
	if utf8.RuneCountInString(got) != 10 {
		t.Fatalf("Shorten(10) = %q (%d runes)", got, utf8.RuneCountInString(got))
	}
}

func TestRunOnEmptyDocument(t *testing.T) {
	d := mustDoc(t, "", textproc.LangGerman)
	if got := Run(d, ForLanguage(textproc.LangGerman), Config{}); got == nil || len(got) != 0 {
		t.Fatalf("empty document = %#v, want an empty non-nil slice", got)
	}
}
