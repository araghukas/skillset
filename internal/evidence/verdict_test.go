package evidence

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestVerdictIntegersAreFrozen pins the on-disk encoding.
//
// Verdicts are stored in SQLite as INTEGER, and the integer is part of the
// signal_rollup PRIMARY KEY as well as its GROUP BY and ON CONFLICT clauses
// (see the schema in evidence.go). Renumbering a constant does not fail to
// compile and does not fail any other test - it silently reinterprets every
// row already written. If this test fails, the fix is to restore the
// numbering, not to update the expectations.
func TestVerdictIntegersAreFrozen(t *testing.T) {
	for _, tc := range []struct {
		verdict Verdict
		want    int
	}{
		{VerdictUnspecified, 0},
		{VerdictApplied, 1},
		{VerdictAppliedWithCorrection, 2},
		{VerdictContradicted, 3},
		{VerdictIncomplete, 4},
		{VerdictNotApplicable, 5},
	} {
		if got := int(tc.verdict); got != tc.want {
			t.Errorf("%s = %d, want %d; renumbering reinterprets existing SQLite rows",
				tc.verdict, got, tc.want)
		}
	}
}

// TestVerdictsCoversEveryReportableConstant guards the iteration order that
// signal serialization depends on.
func TestVerdictsCoversEveryReportableConstant(t *testing.T) {
	if got, want := len(Verdicts), 5; got != want {
		t.Fatalf("len(Verdicts) = %d, want %d; a verdict was added or removed without "+
			"updating Verdicts, which changes how signals serialize", got, want)
	}

	for i, v := range Verdicts {
		if int(v) != i+1 {
			t.Errorf("Verdicts[%d] = %s (%d), want the constant numbered %d; "+
				"Verdicts must stay in ascending numeric order", i, v, int(v), i+1)
		}
		if v == VerdictUnspecified {
			t.Error("Verdicts contains VerdictUnspecified, which is not reportable")
		}
	}
}

func TestVerdictStringRoundTrip(t *testing.T) {
	for _, v := range Verdicts {
		s := v.String()
		if s == "" {
			t.Errorf("%d has no wire form", int(v))
			continue
		}
		got, err := ParseVerdict(s)
		if err != nil {
			t.Errorf("ParseVerdict(%q): %v", s, err)
			continue
		}
		if got != v {
			t.Errorf("ParseVerdict(%q) = %d, want %d", s, int(got), int(v))
		}
	}
}

func TestParseVerdictEmptyIsUnspecified(t *testing.T) {
	got, err := ParseVerdict("")
	if err != nil {
		t.Fatalf("ParseVerdict(\"\"): %v", err)
	}
	if got != VerdictUnspecified {
		t.Errorf("ParseVerdict(\"\") = %d, want VerdictUnspecified", int(got))
	}
}

// The error text is surfaced verbatim to a calling agent, so it has to
// carry enough for the agent to correct itself.
func TestParseVerdictErrorListsValidValues(t *testing.T) {
	_, err := ParseVerdict("APPLIED")
	if err == nil {
		t.Fatal("ParseVerdict(\"APPLIED\") succeeded; verdicts are lower_snake_case")
	}
	for _, name := range VerdictNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention valid verdict %q", err, name)
		}
	}
}

func TestVerdictNamesMatchesVerdictsOrder(t *testing.T) {
	names := VerdictNames()
	if len(names) != len(Verdicts) {
		t.Fatalf("VerdictNames() has %d entries, Verdicts has %d", len(names), len(Verdicts))
	}
	for i, v := range Verdicts {
		if names[i] != v.String() {
			t.Errorf("VerdictNames()[%d] = %q, want %q", i, names[i], v.String())
		}
	}
}

func TestVerdictJSON(t *testing.T) {
	b, err := json.Marshal(VerdictContradicted)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != `"contradicted"` {
		t.Errorf("Marshal(VerdictContradicted) = %s, want \"contradicted\"", b)
	}

	var v Verdict
	if err := json.Unmarshal([]byte(`"not_applicable"`), &v); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if v != VerdictNotApplicable {
		t.Errorf("Unmarshal(\"not_applicable\") = %d, want VerdictNotApplicable", int(v))
	}

	if err := json.Unmarshal([]byte(`"bogus"`), &v); err == nil {
		t.Error("Unmarshal(\"bogus\") succeeded")
	}
}
