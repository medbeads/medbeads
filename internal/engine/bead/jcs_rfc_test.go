package bead

import (
	"testing"

	"github.com/gowebpki/jcs"
)

// Named runes used to build test fixtures below, expressed as Unicode code
// points (via string(rune(...))) rather than literal non-ASCII bytes pasted
// into this source file. This keeps the file itself unambiguous ASCII
// regardless of editor/locale, while still exercising real non-ASCII JSON
// canonicalization behavior end to end.
var (
	runeEuroSign    = string(rune(0x20AC))  // EURO SIGN
	runeEAcute      = string(rune(0x00E9))  // LATIN SMALL LETTER E WITH ACUTE
	runeECircumflex = string(rune(0x00EA))  // LATIN SMALL LETTER E WITH CIRCUMFLEX
	runeODiaeresis  = string(rune(0x00F6))  // LATIN SMALL LETTER O WITH DIAERESIS
	runeHebrewDalet = string(rune(0xFB33))  // HEBREW LETTER DALET WITH DAGESH
	runeControl0080 = string(rune(0x0080))  // C1 control character
	runeDEL         = string(rune(0x007F))  // DELETE
	runeSmileyEmoji = string(rune(0x1F602)) // FACE WITH TEARS OF JOY (a UTF-16 surrogate pair in JSON)

	// newlineEsc / crEsc are the two-character JSON escape sequences
	// backslash-n and backslash-r, built via concatenation so no raw control
	// byte and no ambiguous backslash sequence appears directly in this file.
	backslash  = string(rune(0x5C))
	newlineEsc = backslash + "n"
	crEsc      = backslash + "r"
)

// TestJCS_RFC8785Vectors exercises github.com/gowebpki/jcs directly (not the
// bead-specific Canonicalize wrapper) against representative RFC 8785 test
// vectors, embedded inline (no network access). These mirror the fixtures
// gowebpki/jcs ships under its own testdata input/output directories, which
// in turn come from the official cyberphone/json-canonicalization RFC 8785
// test suite: ES6 number serialization, Unicode object-key sorting (by
// UTF-16 code unit, not locale), and JSON literal/escape handling.
//
// This is the single most important test in the v3 codebase per
// specs/DESIGN_v3.md section 4 (JCS must be verified against RFC test
// vectors; it is called out there as the most critical v3 test): a JCS
// numeric or key-sorting mismatch would silently split Bead IDs for
// semantically identical content.
func TestJCS_RFC8785Vectors(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			// ES6 number serialization: 333333333.33333329 rounds to the
			// nearest representable double; 1E30 becomes 1e+30; 4.50
			// becomes 4.5; 2e-3 becomes 0.002; and
			// 0.000000000000000000000000001 becomes 1e-27. Also exercises
			// string escaping: a redundant escaped-slash sequence is
			// un-escaped to a bare forward slash.
			name: "ES6 number and string serialization (values.json)",
			// Raw string value (before JSON-encoding it below) is:
			//   €$A'B\"/    (backslash, then a literal double-quote, then a slash)
			// JSON-encoded that is:  "€$A'B\\\"\/"
			// which the JCS spec requires to canonicalize to: "€$A'B\\\"/"
			// (the redundant \/ escape is un-escaped to a bare /, everything
			// else is unchanged).
			input: `{"numbers":[333333333.33333329,1E30,4.50,2e-3,` +
				`0.000000000000000000000000001],` +
				`"string":"` + runeEuroSign + `$A'B` + backslash + backslash + backslash + `"` + backslash + `/",` +
				`"literals":[null,true,false]}`,
			want: `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],` +
				`"string":"` + runeEuroSign + `$A'B` + backslash + backslash + backslash + `"/"}`,
		},
		{
			// Key sorting is by UTF-16 code unit ("codepoint order"), which
			// for these French words is NOT the same as French locale
			// collation (peche-with-acute would sort after peche-with-
			// circumflex under French rules, but JCS must ignore locale).
			name: "Unicode key sort ignores locale (french.json)",
			input: `{"peach":"This sorting order",` +
				`"p` + runeEAcute + `ch` + runeEAcute + `":"is wrong according to French",` +
				`"p` + runeECircumflex + `che":"but canonicalization MUST",` +
				`"sin":"ignore locale"}`,
			want: `{"peach":"This sorting order",` +
				`"p` + runeEAcute + `ch` + runeEAcute + `":"is wrong according to French",` +
				`"p` + runeECircumflex + `che":"but canonicalization MUST",` +
				`"sin":"ignore locale"}`,
		},
		{
			// Recursive object-key sorting at every nesting level, empty
			// object/array handling, and a bare 56.0 float rendered as
			// integer 56. The "\n" key below is a literal two-character
			// JSON escape sequence (backslash + n) in the source JSON.
			name: "recursive nested key sort plus integral float (structures.json)",
			input: `{"1":{"f":{"f":"hi","F":5},"` + newlineEsc + `":56.0},` +
				`"10":{},"":"empty","a":{},` +
				`"111":[{"e":"yes","E":"no"}],"A":{}}`,
			want: `{"":"empty","1":{"` + newlineEsc + `":56,"f":{"F":5,"f":"hi"}},` +
				`"10":{},"111":[{"E":"no","e":"yes"}],"A":{},"a":{}}`,
		},
		{
			// Boundary escapes: control chars below codepoint 0x20 (CR, LF)
			// keep the standard short escapes; chars at or above 0x20
			// (including DEL at 0x7F and a surrogate-pair emoji) are emitted
			// literally as UTF-8. Also confirms the numeric-looking key "1"
			// sorts lexicographically (by code unit), not numerically.
			name: "control-char escape boundary plus surrogate pairs (weird.json)",
			input: `{"` + runeEuroSign + `":"Euro Sign","` + crEsc + `":"Carriage Return",` +
				`"` + newlineEsc + `":"Newline","1":"One",` +
				`"` + runeControl0080 + `":"Control` + runeDEL + `","` + runeSmileyEmoji + `":"Smiley",` +
				`"` + runeODiaeresis + `":"Latin Small Letter O With Diaeresis",` +
				`"` + runeHebrewDalet + `":"Hebrew Letter Dalet With Dagesh",` +
				`"</script>":"Browser Challenge"}`,
			want: `{"` + newlineEsc + `":"Newline","` + crEsc + `":"Carriage Return","1":"One",` +
				`"</script>":"Browser Challenge","` + runeControl0080 + `":"Control` + runeDEL + `",` +
				`"` + runeODiaeresis + `":"Latin Small Letter O With Diaeresis",` +
				`"` + runeEuroSign + `":"Euro Sign","` + runeSmileyEmoji + `":"Smiley",` +
				`"` + runeHebrewDalet + `":"Hebrew Letter Dalet With Dagesh"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := jcs.Transform([]byte(tt.input))
			if err != nil {
				t.Fatalf("jcs.Transform: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("jcs.Transform(%s):\n got  = %s\n want = %s", tt.name, got, tt.want)
			}

			// Idempotency: canonicalizing already-canonical JSON is a no-op.
			// This is the property Canonicalize/ComputeID rely on implicitly
			// (re-canonicalizing a Bead's own canonical form must not change
			// its hash).
			twice, err := jcs.Transform(got)
			if err != nil {
				t.Fatalf("jcs.Transform (second pass): %v", err)
			}
			if string(twice) != string(got) {
				t.Errorf("jcs.Transform is not idempotent for %s:\n first  = %s\n second = %s", tt.name, got, twice)
			}
		})
	}
}

// TestJCS_ES6NumberBoundaries drills into the ES6 number-serialization edge
// cases called out explicitly in the task brief: 10.0 becomes 10, 1e+30
// stays exponential, and the 1e-6 boundary between fixed and exponential
// notation (per gowebpki/jcs's NumberToJSON: fixed for 1e-6 <= x < 1e21,
// exponential otherwise). Verified against the actual github.com/gowebpki/jcs
// implementation while writing this test (see task report).
func TestJCS_ES6NumberBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"trailing zero float becomes integer", `10.0`, `10`},
		{"already-integer stays integer", `10`, `10`},
		{"large exponent stays exponential", `1e+30`, `1e+30`},
		{"1e21 is the fixed/exponential crossover (ES6 spec)", `1e21`, `1e+21`},
		{"0.000001 (1e-6) is exactly at the small-number boundary (still fixed)", `0.000001`, `0.000001`},
		{"0.0000001 (1e-7) crosses into exponential notation", `0.0000001`, `1e-7`},
		{"negative zero canonicalizes to 0", `-0`, `0`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := jcs.Transform([]byte(tt.input))
			if err != nil {
				t.Fatalf("jcs.Transform(%s): %v", tt.input, err)
			}
			if string(got) != tt.want {
				t.Errorf("jcs.Transform(%s) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}
