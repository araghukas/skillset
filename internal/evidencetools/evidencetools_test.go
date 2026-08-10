package evidencetools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/araghukas/skillset/internal/evidence"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeResolver struct {
	err error
}

func (r fakeResolver) SkillExistsAt(ctx context.Context, skillName, commit string) error {
	return r.err
}

func testDeps(t *testing.T, verify bool) Deps {
	t.Helper()
	store, err := evidence.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return Deps{Store: store, Resolver: fakeResolver{}, Verify: verify}
}

func connect(t *testing.T, deps Deps) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "skillsd-registry", Version: "test"}, nil)
	Add(srv, deps)

	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		ss.Wait()
	})
	return cs
}

// TestVerdictEnumIsAdvertised is the empirical check the implementation
// plan flagged as unverified: jsonschema-go's struct tag carries a
// description, not a value list, so the enum has to be built by hand and
// spliced into the inferred schema. This confirms it actually lands where
// a client would read it - inside the "skills" array items on
// report_outcome, and at the top level on list_outcome_reports - not just
// that the server starts without panicking.
func TestVerdictEnumIsAdvertised(t *testing.T) {
	cs := connect(t, testDeps(t, false))

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	byName := make(map[string]*mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}

	assertEnum := func(t *testing.T, raw any, path ...string) {
		t.Helper()
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		for _, p := range path {
			next, ok := m[p]
			if !ok {
				t.Fatalf("schema has no %q under path %v: %s", p, path, b)
			}
			nm, ok := next.(map[string]any)
			if !ok {
				t.Fatalf("%q is not an object: %v", p, next)
			}
			m = nm
		}
		enum, ok := m["enum"].([]any)
		if !ok || len(enum) == 0 {
			t.Fatalf("no enum found at %v: %s", path, b)
		}

		got := make([]string, len(enum))
		for i, v := range enum {
			got[i], _ = v.(string)
		}
		want := evidence.VerdictNames()
		if len(got) != len(want) {
			t.Fatalf("enum = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("enum = %v, want %v", got, want)
			}
		}
	}

	t.Run("report_outcome", func(t *testing.T) {
		tool, ok := byName["report_outcome"]
		if !ok {
			t.Fatal("report_outcome not registered")
		}
		assertEnum(t, tool.InputSchema, "properties", "skills", "items", "properties", "verdict")
	})

	t.Run("list_outcome_reports", func(t *testing.T) {
		tool, ok := byName["list_outcome_reports"]
		if !ok {
			t.Fatal("list_outcome_reports not registered")
		}
		assertEnum(t, tool.InputSchema, "properties", "verdict")
	})
}

func TestReportOutcomeRoundTrip(t *testing.T) {
	deps := testDeps(t, false)
	cs := connect(t, deps)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "report_outcome",
		Arguments: map[string]any{
			"report_id":  "r1",
			"agent_id":   "agent-1",
			"session_id": "session-1",
			"skills": []map[string]any{
				{"skill_name": "alpha", "skill_commit": "abc", "verdict": "applied"},
				{"skill_name": "beta", "skill_commit": "def", "verdict": "contradicted", "note": "bash tool errored"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("report_outcome failed: %s", contentText(t, res))
	}
	var out ReportOutcomeOutput
	decodeStructured(t, res, &out)
	if !out.Recorded {
		t.Error("first report should be Recorded=true")
	}

	// Replay with the same report_id: idempotent, Recorded=false.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "report_outcome",
		Arguments: map[string]any{
			"report_id":  "r1",
			"agent_id":   "agent-1",
			"session_id": "session-1",
			"skills": []map[string]any{
				{"skill_name": "alpha", "skill_commit": "abc", "verdict": "applied"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	decodeStructured(t, res, &out)
	if out.Recorded {
		t.Error("replaying the same report_id should return Recorded=false")
	}

	// The signal should reflect what was reported.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_skill_signals",
		Arguments: map[string]any{"skill_name": "beta"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var signals ListSkillSignalsOutput
	decodeStructured(t, res, &signals)
	if len(signals.Signals) != 1 {
		t.Fatalf("expected 1 signal for beta, got %d", len(signals.Signals))
	}
	sig := signals.Signals[0]
	if sig.ReportedSessions != 1 {
		t.Errorf("ReportedSessions = %d, want 1", sig.ReportedSessions)
	}
	if len(sig.VerdictCounts) != 1 || sig.VerdictCounts[0].Verdict != "contradicted" {
		t.Errorf("VerdictCounts = %+v, want one contradicted entry", sig.VerdictCounts)
	}
}

// TestReportOutcomeRejectsUnknownVerdictAsToolError covers the case where
// the enum in the schema does its job: an invalid verdict is rejected by
// schema validation before report_outcome's handler ever runs, and still
// arrives as a tool error (IsError, readable message) rather than a
// protocol error. evidence.ParseVerdict's own error path - used when a
// caller somehow gets past schema validation - is exercised directly in
// internal/evidence's own tests.
func TestReportOutcomeRejectsUnknownVerdictAsToolError(t *testing.T) {
	cs := connect(t, testDeps(t, false))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "report_outcome",
		Arguments: map[string]any{
			"report_id":  "r1",
			"agent_id":   "agent-1",
			"session_id": "session-1",
			"skills": []map[string]any{
				{"skill_name": "alpha", "skill_commit": "abc", "verdict": "SUCCESS"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unknown verdict produced a protocol error rather than a tool error: %v", err)
	}
	if !res.IsError {
		t.Fatal("unknown verdict was accepted")
	}
	text := contentText(t, res)
	if !strings.Contains(text, "applied") {
		t.Errorf("error should list valid verdicts: %q", text)
	}
}

func TestReportOutcomeVerifyRejectsUnknownSkill(t *testing.T) {
	deps := testDeps(t, true)
	deps.Resolver = fakeResolver{err: errNotFound}
	cs := connect(t, deps)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "report_outcome",
		Arguments: map[string]any{
			"report_id":  "r1",
			"agent_id":   "agent-1",
			"session_id": "session-1",
			"skills": []map[string]any{
				{"skill_name": "ghost", "skill_commit": "abc", "verdict": "applied"},
			},
		},
	})
	if err != nil {
		t.Fatalf("verification failure produced a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("report for a nonexistent skill was accepted")
	}
}

var errNotFound = fakeNotFoundError{}

type fakeNotFoundError struct{}

func (fakeNotFoundError) Error() string { return "skill not found at commit" }

func TestListOutcomeReportsRequiresSkillName(t *testing.T) {
	cs := connect(t, testDeps(t, false))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_outcome_reports",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("missing skill_name produced a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("list_outcome_reports without skill_name was accepted")
	}
}

func TestListOutcomeReportsFiltersByVerdict(t *testing.T) {
	deps := testDeps(t, false)
	cs := connect(t, deps)
	ctx := context.Background()

	for i, v := range []string{"applied", "contradicted", "contradicted"} {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "report_outcome",
			Arguments: map[string]any{
				"report_id":  "r" + string(rune('a'+i)),
				"agent_id":   "agent-1",
				"session_id": "session-1",
				"skills": []map[string]any{
					{"skill_name": "alpha", "skill_commit": "abc", "verdict": v, "note": "n"},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_outcome_reports",
		Arguments: map[string]any{"skill_name": "alpha", "verdict": "contradicted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out ListOutcomeReportsOutput
	decodeStructured(t, res, &out)
	if len(out.Reports) != 2 {
		t.Fatalf("expected 2 contradicted reports, got %d", len(out.Reports))
	}
	for _, r := range out.Reports {
		if r.Verdict != "contradicted" {
			t.Errorf("filter leaked a %q report", r.Verdict)
		}
	}
}

func TestListOutcomeReportsRejectsUnknownVerdictFilter(t *testing.T) {
	cs := connect(t, testDeps(t, false))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_outcome_reports",
		Arguments: map[string]any{"skill_name": "alpha", "verdict": "bogus"},
	})
	if err != nil {
		t.Fatalf("unknown verdict filter produced a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("unknown verdict filter was accepted")
	}
}

func TestListOutcomeReportsTruncatesLongNotes(t *testing.T) {
	deps := testDeps(t, false)
	cs := connect(t, deps)
	ctx := context.Background()

	longNote := strings.Repeat("x", maxNoteBytes+500)
	_, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "report_outcome",
		Arguments: map[string]any{
			"report_id":  "r1",
			"agent_id":   "agent-1",
			"session_id": "session-1",
			"skills": []map[string]any{
				{"skill_name": "alpha", "skill_commit": "abc", "verdict": "contradicted", "note": longNote},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_outcome_reports",
		Arguments: map[string]any{"skill_name": "alpha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out ListOutcomeReportsOutput
	decodeStructured(t, res, &out)
	if len(out.Reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(out.Reports))
	}
	if len(out.Reports[0].Note) > maxNoteBytes+20 {
		t.Errorf("note was not truncated: %d bytes", len(out.Reports[0].Note))
	}
}

// TestVerdictSignalOrderingIsDeterministic replaces the coverage that was
// lost when the contiguous enum walk in the old gRPC adapter (`for v :=
// Verdict_VERDICT_APPLIED; v <= Verdict_VERDICT_NOT_APPLICABLE; v++`) was
// ported to a range over evidence.Verdicts. There was no test for this
// behavior before; add one now precisely because it is easy to lose in a
// port and was never caught.
func TestVerdictSignalOrderingIsDeterministic(t *testing.T) {
	deps := testDeps(t, false)
	cs := connect(t, deps)
	ctx := context.Background()

	// Report verdicts out of order, deliberately.
	verdicts := []string{"not_applicable", "applied", "incomplete", "contradicted", "applied_with_correction"}
	for i, v := range verdicts {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "report_outcome",
			Arguments: map[string]any{
				"report_id":  string(rune('a' + i)),
				"agent_id":   "agent-1",
				"session_id": "session-" + string(rune('a'+i)),
				"skills": []map[string]any{
					{"skill_name": "alpha", "skill_commit": "abc", "verdict": v},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	for range 5 {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "list_skill_signals",
			Arguments: map[string]any{"skill_name": "alpha"},
		})
		if err != nil {
			t.Fatal(err)
		}
		var out ListSkillSignalsOutput
		decodeStructured(t, res, &out)
		if len(out.Signals) != 1 {
			t.Fatalf("expected 1 signal, got %d", len(out.Signals))
		}

		got := make([]string, len(out.Signals[0].VerdictCounts))
		for i, vc := range out.Signals[0].VerdictCounts {
			got[i] = vc.Verdict
		}
		want := []string{"applied", "applied_with_correction", "contradicted", "incomplete", "not_applicable"}
		if len(got) != len(want) {
			t.Fatalf("VerdictCounts = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("VerdictCounts = %v, want %v (signal serialization is not deterministic)", got, want)
			}
		}
	}
}

func TestListSkillSignalsMinReportedSessionsDefaultsToOne(t *testing.T) {
	deps := testDeps(t, false)
	cs := connect(t, deps)
	ctx := context.Background()

	_, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "report_outcome",
		Arguments: map[string]any{
			"report_id":  "r1",
			"agent_id":   "agent-1",
			"session_id": "session-1",
			"skills": []map[string]any{
				{"skill_name": "alpha", "skill_commit": "abc", "verdict": "applied"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_skill_signals",
		Arguments: map[string]any{"skill_name": "alpha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out ListSkillSignalsOutput
	decodeStructured(t, res, &out)
	if len(out.Signals) != 1 {
		t.Fatalf("a single reported session should be visible with the default min_reported_sessions, got %d signals", len(out.Signals))
	}
}

func contentText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func decodeStructured(t *testing.T, res *mcp.CallToolResult, into any) {
	t.Helper()
	if res.StructuredContent == nil {
		t.Fatal("result carries no structured content")
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

// TestListOutcomeReportsExplicitEmptyVerdictIsRejected documents a
// consequence of adding the enum: an explicit empty string for "no
// filter" is schema-invalid now, because "" is not one of the five listed
// verdicts. Omitting the field is the supported way to skip the filter -
// the schema tag says so - so this pins that as intentional rather than
// letting it regress silently.
func TestListOutcomeReportsExplicitEmptyVerdictIsRejected(t *testing.T) {
	cs := connect(t, testDeps(t, false))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_outcome_reports",
		Arguments: map[string]any{"skill_name": "alpha", "verdict": ""},
	})
	if err != nil {
		t.Fatalf("explicit empty verdict produced a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an explicit empty verdict string to be rejected by the enum; " +
			"if this now passes, the enum grew a blank entry - check withVerdictEnum")
	}
}
