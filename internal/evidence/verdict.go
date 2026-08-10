package evidence

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Verdicts lists every reportable verdict, in the order signals should
// serialize them. VerdictUnspecified is deliberately absent: it is the
// zero value used to mean "no filter", never a thing an agent reports.
//
// Always range over this rather than writing a contiguous loop over the
// constants. A `for v := VerdictApplied; v <= VerdictNotApplicable; v++`
// silently changes meaning the day a constant is inserted, and the
// serialization order of a signal's verdict counts is part of the contract:
// callers diff successive commits of a skill by eye.
var Verdicts = []Verdict{
	VerdictApplied,
	VerdictAppliedWithCorrection,
	VerdictContradicted,
	VerdictIncomplete,
	VerdictNotApplicable,
}

// verdictNames maps each verdict to its wire form. These strings are the
// API; the integers behind them are storage (see verdict_test.go).
var verdictNames = map[Verdict]string{
	VerdictUnspecified:           "",
	VerdictApplied:               "applied",
	VerdictAppliedWithCorrection: "applied_with_correction",
	VerdictContradicted:          "contradicted",
	VerdictIncomplete:            "incomplete",
	VerdictNotApplicable:         "not_applicable",
}

// String returns the verdict's wire form, or "" for VerdictUnspecified.
func (v Verdict) String() string {
	return verdictNames[v]
}

// VerdictNames returns every valid verdict string in Verdicts order. Use it
// to build error messages and JSON Schema enums, so both stay in step with
// the type automatically.
func VerdictNames() []string {
	out := make([]string, 0, len(Verdicts))
	for _, v := range Verdicts {
		out = append(out, verdictNames[v])
	}
	return out
}

// ParseVerdict converts a wire-form verdict string to its Verdict. The
// empty string parses to VerdictUnspecified, which callers treat as
// "no verdict filter".
//
// The error names every valid value, because it is surfaced verbatim to a
// calling agent as a tool error - the agent should be able to correct
// itself from the message alone without fetching documentation.
func ParseVerdict(s string) (Verdict, error) {
	for v, name := range verdictNames {
		if name == s {
			return v, nil
		}
	}
	return VerdictUnspecified, fmt.Errorf(
		"unknown verdict %q; valid verdicts are %s", s, strings.Join(VerdictNames(), ", "))
}

// MarshalJSON emits the wire-form string rather than the storage integer.
func (v Verdict) MarshalJSON() ([]byte, error) {
	name, ok := verdictNames[v]
	if !ok {
		return nil, fmt.Errorf("evidence: verdict %d has no wire form", int(v))
	}
	return json.Marshal(name)
}

// UnmarshalJSON accepts the wire-form string.
//
// Note that tool input structs deliberately take a plain string and call
// ParseVerdict in the handler instead of relying on this: an error returned
// from UnmarshalJSON fires inside the MCP SDK's argument decoding, before
// the handler runs, and so does not reach the model as a correctable tool
// error. This exists for round-tripping stored data, not for request
// parsing.
func (v *Verdict) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := ParseVerdict(s)
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}
