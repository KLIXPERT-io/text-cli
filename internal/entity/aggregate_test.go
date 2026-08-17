package entity

import "testing"

// doc is a one-line constructor for a document's worth of entities, so the
// aggregation tables below read as data rather than as struct literals.
func doc(ents ...Entity) *Result {
	return &Result{Provider: "fake", Entities: ents}
}

func ent(name, typ string, salience float64, mentions int) Entity {
	return Entity{Name: name, Type: typ, Salience: salience, MentionCount: mentions}
}

func aggNames(aggs []AggregatedEntity) []string {
	out := make([]string, len(aggs))
	for i, a := range aggs {
		out[i] = a.Name
	}
	return out
}

func findAgg(t *testing.T, aggs []AggregatedEntity, name, typ string) AggregatedEntity {
	t.Helper()
	for _, a := range aggs {
		if a.Name == name && a.Type == typ {
			return a
		}
	}
	t.Fatalf("no aggregated entity %q/%s in %v", name, typ, aggNames(aggs))
	return AggregatedEntity{}
}

// TestAggregateArithmetic is the contract: combined salience is the sum across
// documents, avg is that over the documents the entity appeared in, mentions
// sum, and documents count documents rather than occurrences.
func TestAggregateArithmetic(t *testing.T) {
	got := Aggregate([]*Result{
		doc(
			ent("Ada Lovelace", "PERSON", 0.5, 3),
			ent("London", "LOCATION", 0.3, 1),
		),
		doc(
			ent("ada lovelace", "PERSON", 0.2, 1), // case folds into the same entity
			ent("Babbage", "PERSON", 0.6, 2),
		),
		doc(
			ent("  Ada   Lovelace ", "PERSON", 0.1, 1), // whitespace folds too
		),
	})

	ada := findAgg(t, got, "Ada Lovelace", "PERSON")
	if ada.CombinedSalience != 0.8 {
		t.Fatalf("combined_salience = %v, want 0.5+0.2+0.1", ada.CombinedSalience)
	}
	// 0.8 / 3 rounds to four decimals, not to a float artifact.
	if ada.AvgSalience != 0.2667 {
		t.Fatalf("avg_salience = %v, want 0.2667", ada.AvgSalience)
	}
	if ada.Mentions != 5 {
		t.Fatalf("mentions = %d, want 3+1+1", ada.Mentions)
	}
	if ada.Documents != 3 {
		t.Fatalf("documents = %d, want 3", ada.Documents)
	}
	// The first surface form is the reported name, so output does not depend on
	// map iteration order.
	if ada.Name != "Ada Lovelace" {
		t.Fatalf("name = %q, want the first surface form", ada.Name)
	}

	london := findAgg(t, got, "London", "LOCATION")
	if london.Documents != 1 || london.Mentions != 1 || london.CombinedSalience != 0.3 || london.AvgSalience != 0.3 {
		t.Fatalf("non-overlapping entity = %+v", london)
	}
}

// TestAggregateSortOrder pins the exact ordering: combined salience desc, then
// mentions desc, then name asc.
func TestAggregateSortOrder(t *testing.T) {
	got := Aggregate([]*Result{
		doc(
			ent("Zebra", "OTHER", 0.2, 1),
			ent("Alpha", "OTHER", 0.2, 1),
			ent("Middle", "OTHER", 0.2, 4),
			ent("Top", "OTHER", 0.9, 1),
		),
	})
	want := []string{"Top", "Middle", "Alpha", "Zebra"}
	if got := aggNames(got); !eqStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestAggregateKeepsTypesApart is the reason the merge key carries the type:
// adding a company's salience to a product's would invent an importance
// neither has.
func TestAggregateKeepsTypesApart(t *testing.T) {
	got := Aggregate([]*Result{
		doc(ent("Apple", "ORGANIZATION", 0.5, 2)),
		doc(ent("apple", "CONSUMER_GOOD", 0.4, 1)),
	})
	if len(got) != 2 {
		t.Fatalf("merged across types: %+v", got)
	}
	org := findAgg(t, got, "Apple", "ORGANIZATION")
	good := findAgg(t, got, "apple", "CONSUMER_GOOD")
	if org.CombinedSalience != 0.5 || good.CombinedSalience != 0.4 {
		t.Fatalf("salience leaked between types: %+v / %+v", org, good)
	}
	if org.Documents != 1 || good.Documents != 1 {
		t.Fatalf("documents = %d / %d, want 1 each", org.Documents, good.Documents)
	}
}

func TestAggregateSingleAndZeroDocuments(t *testing.T) {
	single := Aggregate([]*Result{doc(ent("Solo", "PERSON", 0.42, 2))})
	if len(single) != 1 {
		t.Fatalf("single document = %+v", single)
	}
	// With one document, combined and average are the same number: the salience
	// itself.
	if single[0].CombinedSalience != 0.42 || single[0].AvgSalience != 0.42 || single[0].Documents != 1 {
		t.Fatalf("single document aggregate = %+v", single[0])
	}

	if got := Aggregate(nil); len(got) != 0 {
		t.Fatalf("Aggregate(nil) = %+v, want empty", got)
	}
	if got := Aggregate([]*Result{}); len(got) != 0 {
		t.Fatalf("Aggregate(empty) = %+v, want empty", got)
	}
	// A nil result in the slice is skipped rather than panicking, and an empty
	// document contributes nothing.
	if got := Aggregate([]*Result{nil, doc()}); len(got) != 0 {
		t.Fatalf("Aggregate(nil result) = %+v, want empty", got)
	}
}

// TestAggregateCountsDocumentsNotOccurrences guards the one case where a
// provider lists the same entity twice within one document.
func TestAggregateCountsDocumentsNotOccurrences(t *testing.T) {
	got := Aggregate([]*Result{
		doc(
			ent("Ada", "PERSON", 0.3, 2),
			ent("ADA", "PERSON", 0.2, 1),
		),
	})
	if len(got) != 1 {
		t.Fatalf("duplicates within one document did not merge: %+v", got)
	}
	a := got[0]
	if a.Documents != 1 {
		t.Fatalf("documents = %d, want 1", a.Documents)
	}
	if a.Mentions != 3 || a.CombinedSalience != 0.5 || a.AvgSalience != 0.5 {
		t.Fatalf("aggregate = %+v", a)
	}
}

// TestAggregateMentionsAtLeastOne keeps `mentions >= documents` true even for a
// provider that reports no mention list at all.
func TestAggregateMentionsAtLeastOne(t *testing.T) {
	got := Aggregate([]*Result{
		doc(Entity{Name: "Silent", Type: "OTHER", Salience: 0.1}),
		doc(Entity{Name: "Silent", Type: "OTHER", Salience: 0.1}),
	})
	if got[0].Mentions != 2 || got[0].Documents != 2 {
		t.Fatalf("aggregate = %+v, want one mention per document", got[0])
	}
}

func TestAggregateLiftsKnowledgeIdentifiers(t *testing.T) {
	got := Aggregate([]*Result{
		doc(Entity{Name: "Ada Lovelace", Type: "PERSON", Salience: 0.1}),
		doc(Entity{
			Name: "Ada Lovelace", Type: "PERSON", Salience: 0.2,
			WikipediaURL: "https://en.wikipedia.org/wiki/Ada_Lovelace",
			MID:          "/m/0ff4d",
		}),
	})
	if got[0].WikipediaURL == "" || got[0].MID == "" {
		t.Fatalf("identifiers lost when only a later document carried them: %+v", got[0])
	}
}

func TestSortAggregatedByMentionsAndName(t *testing.T) {
	aggs := Aggregate([]*Result{
		doc(
			ent("Alpha", "OTHER", 0.9, 1),
			ent("Beta", "OTHER", 0.1, 9),
		),
	})
	SortAggregated(aggs, SortMentions)
	if aggNames(aggs)[0] != "Beta" {
		t.Fatalf("--sort mentions = %v", aggNames(aggs))
	}
	SortAggregated(aggs, SortName)
	if !eqStrings(aggNames(aggs), []string{"Alpha", "Beta"}) {
		t.Fatalf("--sort name = %v", aggNames(aggs))
	}
}

func TestTopAggregated(t *testing.T) {
	aggs := Aggregate([]*Result{doc(
		ent("A", "OTHER", 0.5, 1),
		ent("B", "OTHER", 0.4, 1),
		ent("C", "OTHER", 0.3, 1),
	)})
	if got := TopAggregated(aggs, 2); !eqStrings(aggNames(got), []string{"A", "B"}) {
		t.Fatalf("TopAggregated(2) = %v", aggNames(got))
	}
	if got := TopAggregated(aggs, 0); len(got) != 3 {
		t.Fatalf("TopAggregated(0) = %d, want all", len(got))
	}
	if got := TopAggregated(aggs, 99); len(got) != 3 {
		t.Fatalf("TopAggregated(99) = %d, want all", len(got))
	}
}

func TestRound4(t *testing.T) {
	if got := Round4(float64(float32(0.2))); got != 0.2 {
		t.Fatalf("Round4 = %v, want the float32 noise gone", got)
	}
	if got := Round4(0.123456); got != 0.1235 {
		t.Fatalf("Round4(0.123456) = %v", got)
	}
}
