package index

import "testing"

// TestSearch_TrigramMatchesJapaneseSubstring is R3.3's headline test: FTS5
// with tokenize='trigram' must find a Japanese drug name via a 3-character
// substring taken from the *middle* of the word, not just a prefix — proving
// this is genuinely trigram tokenization (which indexes every overlapping
// 3-char window) rather than e.g. a whitespace/unicode61 tokenizer that
// would only match token-aligned prefixes.
//
// "メロペネム" (meropenem, 5 characters: メ ロ ペ ネ ム) — the middle three
// characters "ロペネ" appear nowhere but inside this word in the indexed
// corpus, so a hit on that substring alone demonstrates trigram behavior.
func TestSearch_TrigramMatchesJapaneseSubstring(t *testing.T) {
	db := openT(t)

	b := testBead(t, "fhir_medicationrequest", "", nil, nil, map[string]any{
		"drug": "メロペネム 1g 点滴静注 8時間毎",
	})
	indexBeadT(t, db, b, BeadLocation{PodPath: "p.pod", PatientRoot: "", Offset: 0, Length: 10})

	results, err := db.Search("ロペネ", 0)
	if err != nil {
		t.Fatalf("Search(middle trigram): %v", err)
	}
	found := false
	for _, r := range results {
		if r.BeadID == b.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("Search(%q) did not match %s (search_text should contain the full drug string): %+v",
			"ロペネ", b.ID, results)
	}
}

// TestSearch_NoMatch checks that a query with no hits returns an empty
// slice and no error, not ErrNotFound (Search's contract is a list API, not
// a single-row lookup).
func TestSearch_NoMatch(t *testing.T) {
	db := openT(t)

	b := testBead(t, "fhir_observation", "unrelated content", nil, nil, nil)
	indexBeadT(t, db, b, BeadLocation{PodPath: "p.pod", PatientRoot: "", Offset: 0, Length: 10})

	results, err := db.Search("完全に無関係な文字列", 0)
	if err != nil {
		t.Fatalf("Search(no match): %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Search(no match) = %+v, want empty", results)
	}
}
