package lint

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// German rules. This is where the yield is for business prose: Passiv,
// Substantivstil, Behördendeutsch.
//
// Every word list this file needs is in the block below, so extending a rule is
// editing one slice — never touching the matching code.

var (
	// deFillers are words that can be deleted without losing meaning.
	deFillers = newPhraseMap(map[string]string{
		"eigentlich":         "streichen",
		"gewissermaßen":      "streichen",
		"sozusagen":          "streichen",
		"quasi":              "streichen",
		"im Grunde":          "streichen",
		"letztendlich":       "streichen, oder „am Ende“",
		"durchaus":           "streichen",
		"relativ":            "streichen, oder die Zahl nennen",
		"sehr":               "streichen, oder ein stärkeres Wort wählen",
		"wirklich":           "streichen",
		"natürlich":          "streichen",
		"selbstverständlich": "streichen",
		"praktisch":          "streichen",
		"im Prinzip":         "streichen",
		"gegebenenfalls":     "streichen, oder die Bedingung nennen",
	})

	// deModalHedges are the Konjunktiv/Modal forms that turn a statement into
	// a maybe. Single uses are fine; a pile-up is the problem.
	deModalHedges = newPhrases(
		"könnte", "könnten", "würde", "würden", "sollte", "sollten",
		"müsste", "müssten", "dürfte", "dürften", "hätte", "hätten",
		"wäre", "wären",
	)

	// deBureaucratic is Behördendeutsch with its plain-language replacement.
	deBureaucratic = newPhraseMap(map[string]string{
		"im Rahmen von":          "„bei“ oder „in“",
		"im Rahmen der":          "„bei“ oder „in“",
		"im Rahmen des":          "„bei“ oder „in“",
		"zum Zwecke der":         "„um … zu“",
		"zum Zwecke des":         "„um … zu“",
		"unter Berücksichtigung": "„mit Blick auf“ oder den Punkt direkt nennen",
		"in Bezug auf":           "„zu“ oder „für“",
		"hinsichtlich":           "„zu“ oder „bei“",
		"bezüglich":              "„zu“",
		"seitens":                "„von“ — und den Handelnden nennen",
		"aufgrund der Tatsache":  "„weil“",
		"nicht zuletzt":          "streichen",
	})

	// deNominalSuffixes mark the Substantivstil, singular and plural.
	deNominalSuffixes = []string{
		"ungen", "ung", "heiten", "heit", "keiten", "keit",
		"nisse", "nis", "tümer", "tum", "schaften", "schaft",
		"ismus", "ismen",
	}

	// deNominalWords are nominalizations no suffix catches.
	deNominalWords = newPhrases(
		"Inanspruchnahme", "Inbetriebnahme", "Kenntnisnahme", "Zurkenntnisnahme",
	)

	// deNominalExceptions end in a nominal suffix but are ordinary nouns; the
	// rule would only be crying wolf on them.
	deNominalExceptions = newPhrases(
		"Zeitung", "Zeitungen", "Wohnung", "Wohnungen", "Rechnung", "Rechnungen",
		"Erfahrung", "Erfahrungen", "Ordnung", "Ordnungen", "Nahrung", "Achtung",
		"Kleidung", "Wahrheit", "Freiheit", "Gesundheit", "Krankheit", "Zeugnis",
		"Ergebnis", "Ergebnisse", "Erlebnis", "Erlebnisse", "Verhältnis",
		"Verhältnisse", "Eigentum", "Reichtum", "Wirtschaft", "Gesellschaft",
		"Landschaft", "Mannschaft", "Botschaft", "Herrschaft", "Wissenschaft",
	)

	// dePassiveAuxiliaries are the werden-forms that carry the passive.
	dePassiveAuxiliaries = newPhrases(
		"werden", "wird", "wurde", "wurden", "worden",
	)

	// dePrefixParticiples are the prefixes whose Partizip II takes no ge-:
	// "bearbeitet", "verwendet", "erstellt", "entwickelt", "zerstört",
	// "empfohlen", "missachtet".
	// "über", "unter" and "wider" behave the same way when they are
	// inseparable: "überarbeitet", "unterzeichnet", "widersprochen".
	dePrefixParticiples = []string{"be", "ver", "er", "ent", "zer", "emp", "miss",
		"über", "unter", "wider"}

	// deSeparablePrefixes sit in front of the ge- of a separable verb:
	// "durchgeführt", "eingesetzt", "vorgenommen".
	deSeparablePrefixes = []string{
		"durch", "zurück", "weiter", "gegen", "unter", "über", "auseinander",
		"zusammen", "voran", "voraus", "hinter", "nach", "vor", "bei", "ein",
		"aus", "auf", "ab", "an", "mit", "um", "zu", "fest", "frei", "los",
		"statt", "teil", "wahr", "weg", "her", "hin", "dar", "empor",
		// Adjective- and noun-plus-verb compounds behave the same way:
		// "sichergestellt", "gleichgestellt", "klargestellt".
		"sicher", "gleich", "klar", "hoch", "heim", "voll", "offen", "wieder",
	}

	// dePartizipIEndings mark a Partizip I or a declined adjective built from
	// one — "bestehenden", "geltende", "entscheidender". They look exactly
	// like a prefixed Partizip II to a suffix test and never are one.
	dePartizipIEndings = []string{"end", "ende", "enden", "endem", "ender", "endes"}

	// deParticipleExceptions look like a Partizip II and are not.
	deParticipleExceptions = newPhrases(
		"gegen", "gegenüber", "gerade", "gern", "gerne", "genug", "gemäß",
		"gesamt", "gestern", "gewiss", "genau", "general", "getreu", "gesund",
		"besonderen", "besonderes", "beiden", "beides", "vielen", "entgegen",
		"verschieden", "verschiedenen", "besten", "ersten", "erst",
	)

	// deFunctionWords are ignored by repeated-word: repeating "und" is not a
	// style problem, repeating "Anforderungen" is.
	deFunctionWords = newPhrases(
		"aber", "alle", "allem", "allen", "aller", "alles", "als", "also", "auch",
		"auf", "aus", "bei", "beim", "bereits", "bin", "bis", "bist", "dabei",
		"damit", "dann", "dass", "dazu", "dem", "den", "denen", "denn", "der",
		"deren", "des", "dessen", "dich", "die", "dies", "diese", "diesem",
		"diesen", "dieser", "dieses", "doch", "dort", "durch", "ein", "eine",
		"einem", "einen", "einer", "eines", "einige", "einigen", "er", "es",
		"etwa", "euch", "für", "gegen", "haben", "hat", "hatte", "hatten", "hier",
		"ihm", "ihn", "ihnen", "ihr", "ihre", "ihrem", "ihren", "ihrer", "ihres",
		"immer", "ist", "jede", "jedem", "jeden", "jeder", "jedes", "jetzt",
		"kann", "kein", "keine", "keinen", "können", "man", "mehr", "mein",
		"mich", "mir", "mit", "muss", "müssen", "nach", "nicht", "nichts", "noch",
		"nun", "nur", "ob", "oder", "ohne", "schon", "sein", "seine", "seinem",
		"seinen", "seiner", "seines", "sich", "sie", "sind", "so", "solche",
		"soll", "sollen", "sondern", "sonst", "über", "um", "und", "uns",
		"unser", "unsere", "unter", "vom", "von", "vor", "war", "waren", "was",
		"weil", "weit", "welche", "welchem", "welchen", "welcher", "welches",
		"wenn", "werden", "wie", "wieder", "wir", "wird", "wirst", "wo", "wurde",
		"wurden", "während", "zum", "zur", "zwar", "zwischen",
	)
)

func init() {
	de := []string{string(textproc.LangGerman)}
	Register(Rule{
		Name:        "passive",
		Title:       "Passiv",
		Description: "werden/wird/wurde/wurden/worden plus Partizip II — der Handelnde fehlt.",
		Languages:   de,
		Severity:    SeverityWarn,
		Check:       checkPassiveDE,
	})
	Register(Rule{
		Name:        "nominalization",
		Title:       "Substantivstil",
		Description: "Dichte an Nominalisierungen (-ung, -heit, -keit, -nis, -tum, -schaft, -ismus).",
		Languages:   de,
		Severity:    SeverityWarn,
		Check:       checkNominalizationDE,
	})
	Register(Rule{
		Name:        "filler",
		Title:       "Füllwörter",
		Description: "Wörter, die ersatzlos gestrichen werden können (eigentlich, sehr, natürlich …).",
		Languages:   de,
		Severity:    SeverityInfo,
		Check:       checkFillerDE,
	})
	Register(Rule{
		Name:        "modal-hedge",
		Title:       "Konjunktiv-Häufung",
		Description: "Dichte an könnte/würde/sollte/müsste/dürfte/hätte/wäre — die Aussage wird unverbindlich.",
		Languages:   de,
		Severity:    SeverityInfo,
		Check:       checkModalHedgeDE,
	})
	Register(Rule{
		Name:        "bureaucratic",
		Title:       "Behördendeutsch",
		Description: "Amtsdeutsche Wendungen mit einfacher Alternative (im Rahmen von, seitens, hinsichtlich …).",
		Languages:   de,
		Severity:    SeverityWarn,
		Check:       checkBureaucraticDE,
	})
}

// checkPassiveDE pairs a werden-form with a Partizip II in the same clause.
//
// German is verb-final, so the participle usually follows the auxiliary at some
// distance ("wird ... vorgenommen") but sits in front of it as soon as a modal
// joins in ("sichergestellt werden kann"). Both directions are checked, and the
// scan stops at a clause boundary so a finding's span is one construction.
//
// This is a heuristic, and it is meant to be. "wird" plus a participle is also
// the Futur ("wird geliefert werden") and the Zustandspassiv, and the participle
// test accepts a few adjectives that end in -t or -en. The rule therefore
// accepts false positives rather than silence: a reader who checks a flagged
// sentence loses five seconds, a reader who never sees the passive keeps
// writing it. Severity stays warn because German business prose that says
// "es wird geprüft" almost always should have said who is doing the checking.
func checkPassiveDE(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	out := []Finding{}
	used := map[int]bool{} // participles already reported, by document position
	for _, sv := range v.sentences {
		for i, t := range sv.Toks {
			if !dePassiveAuxiliaries.has(t.Lower) {
				continue
			}
			p, ok := passiveParticipleDE(d, cfg, sv.Toks, i, used)
			if !ok {
				continue
			}
			used[p.Index] = true
			start, end := t.Start, p.End
			if p.Start < t.Start {
				start, end = p.Start, t.End
			}
			msg := fmt.Sprintf("Passiv: „%s … %s“ — der Handelnde fehlt.", t.Text, p.Text)
			if p.Start < t.Start {
				msg = fmt.Sprintf("Passiv: „%s %s“ — der Handelnde fehlt.", p.Text, t.Text)
			}
			sug := "Aktiv formulieren und das Subjekt nennen: wer tut es?"
			out = append(out, finding(d, "passive", SeverityWarn, sv.Num, start, end, msg, sug, 0))
		}
	}
	return out
}

// passiveParticipleDE finds the participle belonging to the auxiliary at toks[i]
// — forwards to the end of the clause first, then the word immediately before
// the auxiliary, which is where the modal construction puts it.
//
// The forward scan runs backwards from the end of the clause, because German
// puts the participle last: in "wird ... der bestehenden Prozesse vorgenommen"
// the first participle-shaped word is the adjective "bestehenden" and the right
// answer is the final "vorgenommen".
func passiveParticipleDE(d *textproc.Doc, cfg Config, toks []token, i int, used map[int]bool) (token, bool) {
	last := i + cfg.PassiveLookahead
	if last >= len(toks) {
		last = len(toks) - 1
	}
	for j := i + 1; j <= last; j++ {
		if clauseBreak(d, toks[j-1], toks[j]) {
			last = j - 1
			break
		}
	}
	for j := last; j > i; j-- {
		if p := toks[j]; isParticipleDE(p) && !used[p.Index] {
			return p, true
		}
	}
	if i > 0 {
		if p := toks[i-1]; isParticipleDE(p) && !used[p.Index] && !clauseBreak(d, p, toks[i]) {
			return p, true
		}
	}
	return token{}, false
}

// isParticipleDE recognises a Partizip II: ge-…-t / ge-…-en, the prefixed forms
// that take no ge-, and separable verbs that carry the ge- inside
// ("durchgeführt").
func isParticipleDE(t token) bool {
	w := t.Lower
	if r := firstRune(t.Text); r != 0 && unicode.IsUpper(r) {
		return false // a capitalized word mid-sentence is a noun, not a participle
	}
	if !strings.HasSuffix(w, "t") && !strings.HasSuffix(w, "en") {
		return false
	}
	if deParticipleExceptions.has(w) {
		return false
	}
	for _, suf := range dePartizipIEndings {
		if strings.HasSuffix(w, suf) {
			return false
		}
	}
	n := t.Runes
	if strings.HasPrefix(w, "ge") && n >= 5 {
		return true
	}
	for _, p := range dePrefixParticiples {
		if strings.HasPrefix(w, p) && n >= len([]rune(p))+4 {
			return true
		}
	}
	for _, p := range deSeparablePrefixes {
		if strings.HasPrefix(w, p) && strings.HasPrefix(w[len(p):], "ge") && n >= len([]rune(p))+5 {
			return true
		}
	}
	return false
}

// checkNominalizationDE flags the Substantivstil by density, not by occurrence.
// A rule that fires forty times on one page is noise; the writer needs to know
// that the paragraph is built out of nouns, plus a handful of concrete places
// to start.
func checkNominalizationDE(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	hits := []hit{}
	for _, sv := range v.sentences {
		for i, t := range sv.Toks {
			if !isNominalizationDE(t) {
				continue
			}
			h := hit{first: t, last: t, surface: t.Text}
			// "Durchführung von X": the pattern that turns a verb into a noun
			// and then needs a preposition to carry the meaning back. The
			// preposition belongs to the span, because rewriting the noun as a
			// verb deletes it.
			if i+1 < len(sv.Toks) && sv.Toks[i+1].Lower == "von" {
				h.last = sv.Toks[i+1]
			}
			hits = append(hits, h)
		}
	}
	shown, density, ok := dense(cfg, len(v.tokens), hits, cfg.NominalizationDensity)
	if !ok {
		return nil
	}
	pct := strconv.FormatFloat(round(density*100, 1), 'f', -1, 64)
	out := []Finding{}
	for _, h := range shown {
		msg := fmt.Sprintf("Substantivstil: %d Nominalisierungen auf %d Wörter (%s %%). Hier: „%s“.",
			len(hits), len(v.tokens), pct, h.surface)
		sug := "Als Verb schreiben: statt „Durchführung von X“ lieber „X durchführen“."
		out = append(out, finding(d, "nominalization", SeverityWarn, h.first.Sentence,
			h.first.Start, h.last.End, msg, sug, round(density, 4)))
	}
	return out
}

func isNominalizationDE(t token) bool {
	if r := firstRune(t.Text); r == 0 || !unicode.IsUpper(r) {
		return false // nominalizations are nouns, and German nouns are capitalized
	}
	if deNominalWords.has(t.Lower) {
		return true
	}
	if t.Runes < 6 || deNominalExceptions.has(t.Lower) {
		return false
	}
	for _, suf := range deNominalSuffixes {
		if strings.HasSuffix(t.Lower, suf) {
			return true
		}
	}
	return false
}

func checkFillerDE(d *textproc.Doc, cfg Config) []Finding {
	return phraseRule(d, cfg, "filler", SeverityInfo, deFillers,
		func(p phrase, first, last token) (string, string) {
			return fmt.Sprintf("Füllwort: „%s“ trägt nichts zur Aussage bei.", spanText(d, first, last)),
				"Ersatzlos " + fallback(p.Suggestion, "streichen") + "."
		})
}

// checkModalHedgeDE is density-gated: one Konjunktiv is politeness, five in a
// row is a text that does not want to commit to anything.
func checkModalHedgeDE(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	hits := []hit{}
	scanPhrases(v, deModalHedges, func(p phrase, first, last token) {
		hits = append(hits, hit{first: first, last: last, surface: first.Text})
	})
	shown, density, ok := dense(cfg, len(v.tokens), hits, cfg.ModalHedgeDensity)
	if !ok {
		return nil
	}
	out := []Finding{}
	for _, h := range shown {
		msg := fmt.Sprintf("Konjunktiv-Häufung: %d unverbindliche Formen auf %d Wörter. Hier: „%s“.",
			len(hits), len(v.tokens), h.surface)
		sug := "Indikativ schreiben und die Aussage verantworten: „wir prüfen“ statt „wir würden prüfen“."
		out = append(out, finding(d, "modal-hedge", SeverityInfo, h.first.Sentence,
			h.first.Start, h.last.End, msg, sug, round(density, 4)))
	}
	return out
}

func checkBureaucraticDE(d *textproc.Doc, cfg Config) []Finding {
	return phraseRule(d, cfg, "bureaucratic", SeverityWarn, deBureaucratic,
		func(p phrase, first, last token) (string, string) {
			return fmt.Sprintf("Behördendeutsch: „%s“.", spanText(d, first, last)),
				"Einfacher: " + fallback(p.Suggestion, "umformulieren") + "."
		})
}

// phraseRule is the shared body of every per-occurrence word-list rule.
func phraseRule(d *textproc.Doc, cfg Config, rule string, sev Severity, ps *phraseSet,
	msg func(p phrase, first, last token) (string, string)) []Finding {
	v := cfg.tokens(d)
	out := []Finding{}
	scanPhrases(v, ps, func(p phrase, first, last token) {
		m, s := msg(p, first, last)
		out = append(out, finding(d, rule, sev, first.Sentence, first.Start, last.End, m, s, 0))
	})
	return out
}

// spanText is the source text between two tokens, which keeps a multi-word
// phrase's original casing and spacing in the message.
func spanText(d *textproc.Doc, first, last token) string {
	if last.End <= first.Start || last.End > len(d.Text) {
		return first.Text
	}
	return d.Text[first.Start:last.End]
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}
