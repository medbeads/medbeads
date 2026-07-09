package apc

// Config bounds the APC scanner's behavior: match threshold and the five
// runaway-prevention knobs docs/requirements.md R5.2 requires (see Scan's
// doc comment for how each field maps to a specific prevention point).
// Default() returns specs/MEDBEADS_SIBLING_SPEC.md §6.5's own default
// values, so a caller that wants v2-equivalent behavior does not need to set
// anything.
type Config struct {
	// MinScoreThreshold is the minimum match score (see Score) a candidate
	// pair must reach before a sibling_link Bead is generated. Spec default:
	// 4 (§6.4 "生成閾値").
	MinScoreThreshold int

	// MaxSiblingsPerBead caps how many sibling_link Beads may reference a
	// given Bead (runaway-prevention b). Spec default: 10 (§6.5
	// max_siblings_per_bead).
	MaxSiblingsPerBead int

	// MaxGeneration caps sibling_link-of-sibling_link chaining depth
	// (runaway-prevention c): a sibling_link Bead may itself be scanned and
	// matched against other Beads only while its own generation is <
	// MaxGeneration. Spec default: 2 (§6.5 max_sibling_depth).
	MaxGeneration int

	// GenerationDecay is the multiplier applied to a match score once per
	// generation above 0 (runaway-prevention c): score is multiplied by
	// GenerationDecay^generation, where generation is the higher of the two
	// candidate Beads' own scan_generation. Spec default: 0.5 (§6.5
	// secondary_response_decay).
	GenerationDecay float64

	// AntigenFrequencyThreshold is the maximum fraction (0..1) of a
	// patient's Beads an antigen may appear on before it is excluded as a
	// sibling_link trigger (runaway-prevention d, the IDF filter). An
	// antigen at or above this frequency is treated as "everyone has it" —
	// clinically uninformative as a specific match signal and combinatorially
	// explosive as a trigger. DESIGN §7 point 4 gives "例 30%" as the
	// illustrative value; Default() uses that as the concrete default.
	AntigenFrequencyThreshold float64

	// MaxSiblingLinksPerPatientPerScan caps how many sibling_link Beads a
	// single Scan call may generate for one patient (runaway-prevention e,
	// the rate limit DESIGN §7 point 5 calls for: "患者あたり/スキャン周期あた
	// り上限"). Once reached, Scan stops generating further links for that
	// patient for the remainder of this call (already-scanned Beads are
	// still marked scanned, so the next Scan call resumes cleanly — see
	// Scan's doc comment). There is no published spec default for this
	// knob (SPEC §6.5's batch_size/idle_interval describe the v2 daemon's
	// polling loop, not a per-patient generation cap); Default() picks 50 as
	// a generous but finite ceiling — see Default's doc comment for the
	// reasoning.
	MaxSiblingLinksPerPatientPerScan int
}

// Default returns the spec-value configuration: MinScoreThreshold=4,
// MaxSiblingsPerBead=10, MaxGeneration=2, GenerationDecay=0.5,
// AntigenFrequencyThreshold=0.3 (specs/MEDBEADS_SIBLING_SPEC.md §6.4, §6.5;
// specs/DESIGN_v3.md §7). MaxSiblingLinksPerPatientPerScan=50 is this
// package's own choice (not spec-mandated): large enough that it does not
// bind ordinary clinically-dense patients (a few dozen genuine antigen
// co-occurrences per scan is plausible), small enough to bound a single Scan
// call's write volume for one patient if the other four prevention points
// somehow still let a combinatorial blow-up through.
func Default() Config {
	return Config{
		MinScoreThreshold:                4,
		MaxSiblingsPerBead:               10,
		MaxGeneration:                    2,
		GenerationDecay:                  0.5,
		AntigenFrequencyThreshold:        0.3,
		MaxSiblingLinksPerPatientPerScan: 50,
	}
}
