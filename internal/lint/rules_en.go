package lint

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// English rules. Same shape as the German ones: word lists first, matching code
// after, so extending a rule means editing one slice.

var (
	// enFillers are words that can be deleted without losing meaning.
	enFillers = newPhraseMap(map[string]string{
		"very":                    "delete, or pick a stronger word",
		"really":                  "delete",
		"actually":                "delete",
		"basically":               "delete",
		"quite":                   "delete",
		"rather":                  "delete",
		"somewhat":                "delete",
		"just":                    "delete",
		"simply":                  "delete",
		"essentially":             "delete",
		"literally":               "delete",
		"in order to":             `"to"`,
		"at this point in time":   `"now"`,
		"in the event that":       `"if"`,
		"due to the fact that":    `"because"`,
		"for the purpose of":      `"to"`,
		"a large number of":       `"many"`,
		"in spite of the fact":    `"although"`,
		"with regard to":          `"about"`,
		"it should be noted that": "delete and state the point",
	})

	// enHedges soften a claim until it says nothing.
	enHedges = newPhraseMap(map[string]string{
		"might":      "say it plainly, or give the condition",
		"could":      "say it plainly, or give the condition",
		"perhaps":    "delete",
		"possibly":   "delete",
		"seems to":   `"is"`,
		"appears to": `"is"`,
		"tends to":   `"is"`,
		"sort of":    "delete",
		"kind of":    "delete",
	})

	// enBeForms carry the passive.
	enBeForms = newPhrases("is", "are", "was", "were", "been", "being", "be", "am")

	// enIrregularParticiples are the past participles no -ed rule catches.
	enIrregularParticiples = newPhrases(
		"given", "taken", "made", "done", "seen", "written", "known", "held",
		"sent", "built", "brought", "bought", "kept", "left", "told", "found",
		"shown", "driven", "chosen", "spoken", "broken", "forgotten", "hidden",
		"paid", "said", "put", "set", "met", "run", "begun", "gone", "won",
		"understood", "withdrawn", "drawn", "grown", "thrown", "worn", "torn",
		"lost", "sold", "led", "read", "cut", "hit", "let", "split", "spread",
		"taught", "caught", "felt", "meant", "dealt", "sung", "sunk", "struck",
	)

	// enNominalSuffixes mark a verb turned into a noun.
	enNominalSuffixes = []string{"tions", "tion", "ments", "ment", "ances", "ance",
		"ences", "ence", "ities", "ity", "nesses", "ness"}

	// enNominalExceptions end in a nominal suffix and are not one.
	enNominalExceptions = newPhrases(
		"moment", "moments", "element", "elements", "sentence", "sentences",
		"science", "sciences", "silence", "audience", "audiences", "evidence",
		"distance", "distances", "instance", "instances", "business",
		"businesses", "witness", "fitness", "illness", "city", "cities",
		"entity", "entities", "quality", "qualities", "quantity", "quantities",
		"security", "community", "communities", "university", "universities",
		"experience", "experiences", "difference", "differences", "document",
		"documents", "environment", "environments", "equipment", "government",
		"governments", "question", "questions", "section", "sections",
	)

	// enAdverbExceptions end in -ly and are not adverbs.
	enAdverbExceptions = newPhrases(
		"only", "family", "families", "reply", "replies", "apply", "applies",
		"supply", "supplies", "early", "likely", "ugly", "holy", "july", "italy",
		"rally", "ally", "belly", "jelly", "silly", "lily", "monopoly", "anomaly",
		"assembly", "multiply", "imply", "comply", "rely", "fly", "ply", "duly",
		"daily", "weekly", "monthly", "yearly", "friendly", "lonely", "lovely",
		"deadly", "costly", "unlikely", "italy",
	)

	// enFunctionWords are ignored by repeated-word.
	enFunctionWords = newPhrases(
		"about", "after", "again", "against", "all", "also", "among", "and",
		"another", "any", "are", "because", "been", "before", "being", "below",
		"between", "both", "but", "came", "can", "come", "could", "did", "does",
		"doing", "done", "down", "during", "each", "either", "else", "even",
		"ever", "every", "few", "for", "from", "further", "had", "has", "have",
		"having", "her", "here", "hers", "him", "his", "how", "however", "into",
		"its", "itself", "just", "least", "less", "like", "made", "make", "many",
		"may", "might", "more", "most", "much", "must", "neither", "never",
		"next", "nor", "not", "now", "off", "often", "once", "one", "only",
		"onto", "other", "others", "our", "ours", "out", "over", "own", "per",
		"quite", "rather", "same", "shall", "she", "should", "since", "some",
		"still", "such", "than", "that", "the", "their", "theirs", "them",
		"then", "there", "these", "they", "this", "those", "though", "through",
		"thus", "too", "under", "until", "upon", "very", "was", "were", "what",
		"when", "where", "whether", "which", "while", "who", "whom", "whose",
		"why", "will", "with", "within", "without", "would", "you", "your",
		"yours",
	)
)

func init() {
	en := []string{string(textproc.LangEnglish)}
	Register(Rule{
		Name:        "passive",
		Title:       "Passive voice",
		Description: "A be-form plus a past participle — the actor is missing.",
		Languages:   en,
		Severity:    SeverityWarn,
		Check:       checkPassiveEN,
	})
	Register(Rule{
		Name:        "filler",
		Title:       "Filler",
		Description: "Words that can be deleted without losing meaning (very, really, basically …).",
		Languages:   en,
		Severity:    SeverityInfo,
		Check:       checkFillerEN,
	})
	Register(Rule{
		Name:        "hedge",
		Title:       "Hedge",
		Description: "might, could, perhaps, seems to … — a claim softened until it says nothing.",
		Languages:   en,
		Severity:    SeverityInfo,
		Check:       checkHedgeEN,
	})
	Register(Rule{
		Name:        "nominalization",
		Title:       "Nominalization",
		Description: "Density of -tion, -ment, -ance, -ence, -ity, -ness nouns hiding a verb.",
		Languages:   en,
		Severity:    SeverityWarn,
		Check:       checkNominalizationEN,
	})
	Register(Rule{
		Name:        "adverb",
		Title:       "Adverbs",
		Description: "Density of -ly adverbs propping up weak verbs.",
		Languages:   en,
		Severity:    SeverityInfo,
		Check:       checkAdverbEN,
	})
}

// checkPassiveEN pairs a be-form with a following past participle inside the
// same sentence. Like the German rule it accepts false positives ("is tired",
// "was pleased"): a flagged sentence costs the reader a glance, an unflagged
// passive costs the document its actor.
func checkPassiveEN(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	out := []Finding{}
	for _, sv := range v.sentences {
		for i, t := range sv.Toks {
			if !enBeForms.has(t.Lower) {
				continue
			}
			// English keeps the participle next to the auxiliary, so the scan
			// is short and stops at the first clause boundary.
			last := i + 4
			if last >= len(sv.Toks) {
				last = len(sv.Toks) - 1
			}
			for j := i + 1; j <= last; j++ {
				if clauseBreak(d, sv.Toks[j-1], sv.Toks[j]) {
					break
				}
				p := sv.Toks[j]
				if !isParticipleEN(p) {
					continue
				}
				msg := fmt.Sprintf("Passive voice: %q … %q — the actor is missing.", t.Text, p.Text)
				sug := "Name the actor and use the active voice: who does it?"
				out = append(out, finding(d, "passive", SeverityWarn, sv.Num, t.Start, p.End, msg, sug, 0))
				break
			}
		}
	}
	return out
}

func isParticipleEN(t token) bool {
	w := t.Lower
	if enIrregularParticiples.has(w) {
		return true
	}
	// "-ed" plus a minimum length, so "red" and "bed" stay out.
	return strings.HasSuffix(w, "ed") && t.Runes >= 5
}

func checkFillerEN(d *textproc.Doc, cfg Config) []Finding {
	return phraseRule(d, cfg, "filler", SeverityInfo, enFillers,
		func(p phrase, first, last token) (string, string) {
			return fmt.Sprintf("Filler: %q adds nothing.", spanText(d, first, last)),
				"Use " + fallback(p.Suggestion, "delete") + "."
		})
}

func checkHedgeEN(d *textproc.Doc, cfg Config) []Finding {
	return phraseRule(d, cfg, "hedge", SeverityInfo, enHedges,
		func(p phrase, first, last token) (string, string) {
			return fmt.Sprintf("Hedge: %q weakens the claim.", spanText(d, first, last)),
				"Use " + fallback(p.Suggestion, "a direct statement") + "."
		})
}

// checkNominalizationEN is density-gated for the same reason as the German
// rule: one nominalization is a word choice, twenty is the style of the text.
func checkNominalizationEN(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	hits := []hit{}
	for _, t := range v.tokens {
		if isNominalizationEN(t) {
			hits = append(hits, hit{first: t, last: t, surface: t.Text})
		}
	}
	shown, density, ok := dense(cfg, len(v.tokens), hits, cfg.NominalizationDensity)
	if !ok {
		return nil
	}
	pct := strconv.FormatFloat(round(density*100, 1), 'f', -1, 64)
	out := []Finding{}
	for _, h := range shown {
		msg := fmt.Sprintf("Nominalization: %d noun forms in %d words (%s%%). Here: %q.",
			len(hits), len(v.tokens), pct, h.surface)
		sug := `Use the verb: "we decided" rather than "the decision was taken".`
		out = append(out, finding(d, "nominalization", SeverityWarn, h.first.Sentence,
			h.first.Start, h.last.End, msg, sug, round(density, 4)))
	}
	return out
}

func isNominalizationEN(t token) bool {
	if t.Runes < 7 || enNominalExceptions.has(t.Lower) {
		return false
	}
	for _, suf := range enNominalSuffixes {
		if strings.HasSuffix(t.Lower, suf) {
			return true
		}
	}
	return false
}

// checkAdverbEN is density-gated: an -ly adverb now and then is fine, a text
// full of them is a text propping up weak verbs.
func checkAdverbEN(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	hits := []hit{}
	for _, t := range v.tokens {
		if t.Runes >= 5 && strings.HasSuffix(t.Lower, "ly") && !enAdverbExceptions.has(t.Lower) {
			hits = append(hits, hit{first: t, last: t, surface: t.Text})
		}
	}
	shown, density, ok := dense(cfg, len(v.tokens), hits, cfg.AdverbDensity)
	if !ok {
		return nil
	}
	pct := strconv.FormatFloat(round(density*100, 1), 'f', -1, 64)
	out := []Finding{}
	for _, h := range shown {
		msg := fmt.Sprintf("Adverbs: %d -ly adverbs in %d words (%s%%). Here: %q.",
			len(hits), len(v.tokens), pct, h.surface)
		sug := `Pick a stronger verb: "sprinted" rather than "ran quickly".`
		out = append(out, finding(d, "adverb", SeverityInfo, h.first.Sentence,
			h.first.Start, h.last.End, msg, sug, round(density, 4)))
	}
	return out
}
