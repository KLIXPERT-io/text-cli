package lint

import (
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// Plain German prose of the same length as the positive cases, so a rule that
// fires here is firing on nothing.
const plainDE = "Die Abteilung prüft die Anträge. Wir schicken die Antwort am Montag. " +
	"Das Team meldet den Stand jede Woche. Wir rufen die Kunden an, wenn wir etwas brauchen. " +
	"Danach schließen wir den Vorgang und legen die Unterlagen ab."

func TestPassiveDE(t *testing.T) {
	runRuleCases(t, "passive", []ruleCase{
		{
			name: "werden plus Partizip II",
			text: "Die Anträge werden von der Abteilung geprüft.", lang: textproc.LangGerman,
			want: []string{"werden von der Abteilung geprüft"}, sev: SeverityWarn,
		},
		{
			name: "the participle may precede the auxiliary",
			text: "Die Einhaltung der Fristen muss sichergestellt werden.", lang: textproc.LangGerman,
			want: []string{"sichergestellt werden"},
		},
		{
			name: "prefixed participles take no ge-",
			text: "Der Vertrag wurde von der Kanzlei überarbeitet.", lang: textproc.LangGerman,
			want: []string{"wurde von der Kanzlei überarbeitet"},
		},
		{
			name: "separable verbs carry the ge- inside",
			text: "Die Prüfung wird kommende Woche durchgeführt.", lang: textproc.LangGerman,
			want: []string{"wird kommende Woche durchgeführt"},
		},
		{
			name: "active voice is silent",
			text: "Die Abteilung prüft die Anträge der Kunden.", lang: textproc.LangGerman,
			want: nil,
		},
		{
			name: "Futur is not Passiv",
			text: "Wir werden die Anträge morgen prüfen.", lang: textproc.LangGerman,
			want: nil,
		},
		{
			name: "a declined Partizip I is not a Partizip II",
			text: "Der Bericht wird über die bestehenden Regelungen informieren.", lang: textproc.LangGerman,
			want: nil,
		},
		{
			name: "plain prose stays quiet",
			text: plainDE, lang: textproc.LangGerman,
			want: nil,
		},
	})

	fs := runRule(t, "Die Anträge werden von der Abteilung geprüft.", textproc.LangGerman, "passive", Config{})
	if !strings.Contains(fs[0].Message, "Passiv") || !strings.Contains(fs[0].Suggestion, "Aktiv") {
		t.Fatalf("message/suggestion = %q / %q", fs[0].Message, fs[0].Suggestion)
	}
}

func TestNominalizationDE(t *testing.T) {
	dense := "Die Durchführung von Schulungen erfolgt nach Anmeldung. Die Bearbeitung der Anträge " +
		"und die Prüfung der Unterlagen liegen in der Verantwortung der Fachabteilung. Die " +
		"Genehmigung der Auszahlung setzt die Vorlage der Bescheinigung voraus. Die Einhaltung " +
		"der Vorgaben unterliegt der Kontrolle der Leitung des Bereichs."

	fs := runRule(t, dense, textproc.LangGerman, "nominalization", Config{})
	if len(fs) == 0 {
		t.Fatal("a paragraph built out of nouns produced no finding")
	}
	if len(fs) > Defaults().MaxDensityFindings {
		t.Fatalf("%d findings: a density rule must not fire on every occurrence", len(fs))
	}
	if fs[0].Excerpt != "Durchführung von" {
		t.Fatalf("the „Durchführung von“ pattern should span the preposition, got %q", fs[0].Excerpt)
	}
	if !strings.Contains(fs[0].Message, "Nominalisierungen") || !strings.Contains(fs[0].Suggestion, "Verb") {
		t.Fatalf("message/suggestion = %q / %q", fs[0].Message, fs[0].Suggestion)
	}
	if fs[0].Value <= 0 {
		t.Fatalf("value should carry the density, got %v", fs[0].Value)
	}

	// A verb-driven text of the same length says nothing, even though it
	// contains a nominalization or two.
	if got := runRule(t, plainDE, textproc.LangGerman, "nominalization", Config{}); len(got) != 0 {
		t.Fatalf("plain prose produced %d nominalization findings: %v", len(got), excerpts(got))
	}
	// Everyday nouns that merely end in a nominal suffix are not the
	// Substantivstil.
	ordinary := "Die Zeitung liegt auf dem Tisch. Die Wohnung ist hell und ruhig. " +
		"Meine Erfahrung mit dem Gerät ist gut. Die Gesellschaft lädt zur Feier. " +
		"Das Ergebnis der Rechnung stimmt. Die Wahrheit ist einfach."
	if got := runRule(t, ordinary, textproc.LangGerman, "nominalization", Config{}); len(got) != 0 {
		t.Fatalf("ordinary nouns were flagged: %v", excerpts(got))
	}
}

func TestFillerDE(t *testing.T) {
	runRuleCases(t, "filler", []ruleCase{
		{
			name: "single words and phrases",
			text: "Das ist eigentlich sehr gut und im Grunde natürlich richtig.", lang: textproc.LangGerman,
			want: []string{"eigentlich", "sehr", "im Grunde", "natürlich"}, sev: SeverityInfo,
		},
		{
			name: "clean prose",
			text: plainDE, lang: textproc.LangGerman,
			want: nil,
		},
	})
	fs := runRule(t, "Das ist im Prinzip gut.", textproc.LangGerman, "filler", Config{})
	if len(fs) != 1 || !strings.Contains(fs[0].Suggestion, "streichen") {
		t.Fatalf("filler = %+v", fs)
	}
}

func TestModalHedgeDE(t *testing.T) {
	hedged := "Wir könnten die Anträge prüfen, sofern die Unterlagen vorliegen würden. " +
		"Das Team sollte den Bericht schreiben, damit die Leitung entscheiden könnte. " +
		"Es wäre gut, wenn die Frist verlängert würde und niemand von uns darauf lange warten müsste. " +
		"Der Termin dürfte am Ende auch später liegen."
	fs := runRule(t, hedged, textproc.LangGerman, "modal-hedge", Config{})
	if len(fs) == 0 {
		t.Fatal("a paragraph in the Konjunktiv produced no finding")
	}
	if len(fs) > Defaults().MaxDensityFindings {
		t.Fatalf("%d findings, want at most %d", len(fs), Defaults().MaxDensityFindings)
	}
	if fs[0].Excerpt != "könnten" || !strings.Contains(fs[0].Suggestion, "Indikativ") {
		t.Fatalf("first finding = %+v", fs[0])
	}

	// One subjunctive in a paragraph is politeness, not a problem.
	single := "Wir prüfen die Anträge und schicken die Antwort. Das Team meldet den Stand " +
		"jede Woche. Vielleicht könnte der Termin am Montag stattfinden. Danach schließen " +
		"wir den Vorgang und legen die Unterlagen ab."
	if got := runRule(t, single, textproc.LangGerman, "modal-hedge", Config{}); len(got) != 0 {
		t.Fatalf("a single Konjunktiv was flagged: %v", excerpts(got))
	}
}

func TestBureaucraticDE(t *testing.T) {
	runRuleCases(t, "bureaucratic", []ruleCase{
		{
			name: "phrases and single words",
			text: "Im Rahmen der Prüfung wird seitens der Abteilung hinsichtlich der Fristen entschieden.",
			lang: textproc.LangGerman,
			want: []string{"Im Rahmen der", "seitens", "hinsichtlich"}, sev: SeverityWarn,
		},
		{
			name: "the longest phrase wins",
			text: "Die Entscheidung erfolgt unter Berücksichtigung der Vorgaben.", lang: textproc.LangGerman,
			want: []string{"unter Berücksichtigung"},
		},
		{
			name: "plain German is not Behördendeutsch",
			text: plainDE, lang: textproc.LangGerman,
			want: nil,
		},
	})

	fs := runRule(t, "Aufgrund der Tatsache, dass die Frist läuft, entscheiden wir heute.",
		textproc.LangGerman, "bureaucratic", Config{})
	if len(fs) != 1 || fs[0].Excerpt != "Aufgrund der Tatsache" {
		t.Fatalf("bureaucratic = %+v", fs)
	}
	if !strings.Contains(fs[0].Suggestion, "weil") {
		t.Fatalf("the suggestion must name the plain-language replacement: %q", fs[0].Suggestion)
	}
}
