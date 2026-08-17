package lint

import (
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// Plain English prose of the same length as the positive cases.
const plainEN = "The team checks the forms. We send the answer on Monday. " +
	"The group reports the state of the work each week. We call the client when we need a fact. " +
	"Then we close the case and file the papers."

func TestPassiveEN(t *testing.T) {
	runRuleCases(t, "passive", []ruleCase{
		{
			name: "be-form plus -ed participle",
			text: "The report was reviewed by the committee.", lang: textproc.LangEnglish,
			want: []string{"was reviewed"}, sev: SeverityWarn,
		},
		{
			name: "irregular participles",
			text: "The decision was taken last week.", lang: textproc.LangEnglish,
			want: []string{"was taken"},
		},
		{
			name: "the auxiliary may be separated",
			text: "The forms are usually checked twice.", lang: textproc.LangEnglish,
			want: []string{"are usually checked"},
		},
		{
			name: "active voice is silent",
			text: "The committee reviewed the report.", lang: textproc.LangEnglish,
			want: nil,
		},
		{
			name: "the scan stops at a clause boundary",
			text: "The room is small, and the team finished the report.", lang: textproc.LangEnglish,
			want: nil,
		},
		{
			name: "plain prose stays quiet",
			text: plainEN, lang: textproc.LangEnglish,
			want: nil,
		},
	})
}

func TestFillerEN(t *testing.T) {
	runRuleCases(t, "filler", []ruleCase{
		{
			name: "words and phrases",
			text: "This is actually very good, and we did it in order to save time.",
			lang: textproc.LangEnglish,
			want: []string{"actually", "very", "in order to"}, sev: SeverityInfo,
		},
		{
			name: "clean prose",
			text: plainEN, lang: textproc.LangEnglish,
			want: nil,
		},
	})
	fs := runRule(t, "We will decide at this point in time.", textproc.LangEnglish, "filler", Config{})
	if len(fs) != 1 || fs[0].Excerpt != "at this point in time" {
		t.Fatalf("filler = %+v", fs)
	}
	if !strings.Contains(fs[0].Suggestion, "now") {
		t.Fatalf("the suggestion must offer the replacement: %q", fs[0].Suggestion)
	}
}

func TestHedgeEN(t *testing.T) {
	runRuleCases(t, "hedge", []ruleCase{
		{
			name: "single words and two-word hedges",
			text: "This might work, and the plan seems to hold, though perhaps not.",
			lang: textproc.LangEnglish,
			want: []string{"might", "seems to", "perhaps"}, sev: SeverityInfo,
		},
		{
			name: "a direct claim is silent",
			text: plainEN, lang: textproc.LangEnglish,
			want: nil,
		},
	})
}

func TestNominalizationEN(t *testing.T) {
	dense := "The implementation of the configuration requires the completion of the registration. " +
		"The evaluation of the documentation depends on the availability of the specification. " +
		"The distribution of the information follows the confirmation of the participation. " +
		"The preparation of the presentation waits on the approval of the reservation."
	fs := runRule(t, dense, textproc.LangEnglish, "nominalization", Config{})
	if len(fs) == 0 {
		t.Fatal("a paragraph of noun forms produced no finding")
	}
	if len(fs) > Defaults().MaxDensityFindings {
		t.Fatalf("%d findings: a density rule must not fire on every occurrence", len(fs))
	}
	if fs[0].Excerpt != "implementation" {
		t.Fatalf("first finding = %q", fs[0].Excerpt)
	}
	if !strings.Contains(fs[0].Suggestion, "verb") {
		t.Fatalf("the suggestion must point at the verb: %q", fs[0].Suggestion)
	}
	if got := runRule(t, plainEN, textproc.LangEnglish, "nominalization", Config{}); len(got) != 0 {
		t.Fatalf("plain prose was flagged: %v", excerpts(got))
	}
	// Words that merely end in a nominal suffix are not nominalizations.
	ordinary := "The moment passed and the element stayed in place. The city is quiet at night " +
		"and the business is closed. The audience left the science museum. " +
		"The distance to the university is short. The evidence is on the desk."
	if got := runRule(t, ordinary, textproc.LangEnglish, "nominalization", Config{}); len(got) != 0 {
		t.Fatalf("ordinary nouns were flagged: %v", excerpts(got))
	}
}

func TestAdverbEN(t *testing.T) {
	dense := "The team quickly finished the work and carefully checked the numbers. " +
		"We really wanted the report to land properly and to read clearly. " +
		"The client responded immediately and the plan changed slightly again. " +
		"We happily rewrote the summary and gladly sent it back."
	fs := runRule(t, dense, textproc.LangEnglish, "adverb", Config{})
	if len(fs) == 0 {
		t.Fatal("a paragraph of -ly adverbs produced no finding")
	}
	if len(fs) > Defaults().MaxDensityFindings {
		t.Fatalf("%d findings, want at most %d", len(fs), Defaults().MaxDensityFindings)
	}
	if fs[0].Excerpt != "quickly" || !strings.Contains(fs[0].Suggestion, "verb") {
		t.Fatalf("first finding = %+v", fs[0])
	}
	if got := runRule(t, plainEN, textproc.LangEnglish, "adverb", Config{}); len(got) != 0 {
		t.Fatalf("plain prose was flagged: %v", excerpts(got))
	}
	// -ly words that are not adverbs stay out, however many there are.
	nouns := "The family reply came early and the supply was likely to hold. " +
		"The assembly is friendly and the monthly rally is lovely. " +
		"The daily supply of the family is costly but the reply is friendly."
	if got := runRule(t, nouns, textproc.LangEnglish, "adverb", Config{}); len(got) != 0 {
		t.Fatalf("non-adverbs were flagged: %v", excerpts(got))
	}
}

// The English rules must not run on German and vice versa, or a German
// document would be told its nouns end in -tion.
func TestEnglishRulesDoNotApplyToGerman(t *testing.T) {
	for _, name := range []string{"adverb", "hedge"} {
		if _, known, supported := GetFor(name, textproc.LangGerman); !known || supported {
			t.Fatalf("%s on de: known=%v supported=%v", name, known, supported)
		}
	}
	for _, name := range []string{"bureaucratic", "modal-hedge"} {
		if _, known, supported := GetFor(name, textproc.LangEnglish); !known || supported {
			t.Fatalf("%s on en: known=%v supported=%v", name, known, supported)
		}
	}
}
