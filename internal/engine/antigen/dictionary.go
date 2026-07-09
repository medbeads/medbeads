package antigen

import (
	"embed"
	"encoding/json"
	"fmt"
)

// dictionaryFS embeds the static antigen derivation dictionary
// (dictionary.json). See doc.go for the "LLM drafts offline, human reviews
// and commits, Extract applies deterministically" policy this file exists
// to enforce: nothing in this package's runtime path ever calls an LLM or
// performs network I/O to derive an antigen.
//
//go:embed dictionary.json
var dictionaryFS embed.FS

// dictionaryEntry is one RxNorm code's set of derived antigens, as stored in
// dictionary.json. Fields are plain string slices (already namespaced, e.g.
// "atc:c09aa03") rather than bare codes, so dictionary.json is the single
// place that decides both the derived antigen's namespace and its value.
type dictionaryEntry struct {
	Display string   `json:"display"`
	ATC     []string `json:"atc"`
	Organ   []string `json:"organ"`
	Risk    []string `json:"risk"`
}

// dictionary is the parsed form of dictionary.json.
type dictionary struct {
	Version int                        `json:"version"`
	RxNorm  map[string]dictionaryEntry `json:"rxnorm"`
}

// loadDictionary parses the embedded dictionary.json. It is called once, at
// package init (see the package-level dict variable below), so Extract's
// hot path never re-parses JSON — this is purely a load-time step, not part
// of Extract's per-call cost, and has no bearing on Extract's determinism
// (the embedded bytes are fixed at compile time, so every process loads the
// exact same dictionary).
func loadDictionary() (dictionary, error) {
	raw, err := dictionaryFS.ReadFile("dictionary.json")
	if err != nil {
		return dictionary{}, fmt.Errorf("antigen: read embedded dictionary.json: %w", err)
	}
	var d dictionary
	if err := json.Unmarshal(raw, &d); err != nil {
		return dictionary{}, fmt.Errorf("antigen: parse embedded dictionary.json: %w", err)
	}
	if d.Version < 1 {
		return dictionary{}, fmt.Errorf("antigen: embedded dictionary.json: missing or invalid version field")
	}
	return d, nil
}

// dict is the package-level parsed dictionary, loaded once at init from the
// embedded dictionary.json. A parse failure here is a build-time defect
// (the file is compiled into the binary), so it panics at init rather than
// forcing every Extract call site to handle an error that can only occur if
// this package itself ships a malformed embedded asset.
var dict = func() dictionary {
	d, err := loadDictionary()
	if err != nil {
		panic(err)
	}
	return d
}()

// deriveFromRxNorm returns the dictionary-derived antigens (atc:/organ:/
// risk:) for a bare RxNorm code (no "rxnorm:" prefix), or nil if the code is
// not in the dictionary. The dictionary is a Go map, so iteration order is
// randomized by the runtime; deriveFromRxNorm never relies on map iteration
// order — it looks up the single entry for code and returns its three
// slices in a fixed field order (atc, organ, risk), which Extract then
// dedups and sorts along with everything else.
func deriveFromRxNorm(code string) []string {
	entry, ok := dict.RxNorm[code]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(entry.ATC)+len(entry.Organ)+len(entry.Risk))
	out = append(out, entry.ATC...)
	out = append(out, entry.Organ...)
	out = append(out, entry.Risk...)
	return out
}
