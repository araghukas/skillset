package evidence

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func report(id, agent, session string, outcomes ...SkillOutcome) Report {
	return Report{ReportID: id, AgentID: agent, SessionID: session, Skills: outcomes}
}

func TestRecordReportIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	r := report("report-1", "agent-1", "session-1",
		SkillOutcome{SkillName: "deploy", SkillCommit: "abc123", Verdict: VerdictApplied})

	recorded, err := store.RecordReport(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if !recorded {
		t.Fatal("expected the first submission to be recorded")
	}

	// A retry through a registry restart must not double-count.
	recorded, err = store.RecordReport(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Fatal("expected a replayed report_id to be reported as already stored")
	}

	signals, err := store.ListSignals(ctx, "deploy", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 || signals[0].ReportedSessions != 1 {
		t.Fatalf("expected the replay to leave exactly 1 session, got %+v", signals)
	}
}

func TestRecordReportRequiresACommit(t *testing.T) {
	store := newTestStore(t)

	_, err := store.RecordReport(context.Background(),
		report("r", "agent-1", "s", SkillOutcome{SkillName: "deploy", Verdict: VerdictApplied}))
	if err == nil {
		t.Fatal("expected a report with no skill_commit to be rejected: it can't be attributed to a version")
	}
}

func TestRecordReportRequiresAVerdict(t *testing.T) {
	store := newTestStore(t)

	_, err := store.RecordReport(context.Background(),
		report("r", "agent-1", "s", SkillOutcome{SkillName: "deploy", SkillCommit: "abc"}))
	if err == nil {
		t.Fatal("expected a report with no verdict to be rejected")
	}
}

func TestSignalRatesSeparateContentDefectsFromMistriggering(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Ten sessions on one commit: 2 contradicted, 1 incomplete (content
	// defects), 4 not-applicable (a description problem), 3 clean.
	verdicts := []Verdict{
		VerdictContradicted, VerdictContradicted, VerdictIncomplete,
		VerdictNotApplicable, VerdictNotApplicable, VerdictNotApplicable, VerdictNotApplicable,
		VerdictApplied, VerdictApplied, VerdictApplied,
	}
	for i, v := range verdicts {
		id := string(rune('a' + i))
		_, err := store.RecordReport(ctx, report("report-"+id, "agent-"+id, "session-"+id,
			SkillOutcome{SkillName: "deploy", SkillCommit: "abc123", Verdict: v}))
		if err != nil {
			t.Fatal(err)
		}
	}

	signals, err := store.ListSignals(ctx, "deploy", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal row, got %d", len(signals))
	}
	s := signals[0]

	if s.ReportedSessions != 10 {
		t.Fatalf("expected 10 reported sessions, got %d", s.ReportedSessions)
	}
	if s.DefectRate != 0.3 {
		t.Fatalf("expected a defect rate of 0.3 (2 contradicted + 1 incomplete), got %v", s.DefectRate)
	}
	// The two rates must not be conflated: they imply different repairs.
	if s.NotApplicableRate != 0.4 {
		t.Fatalf("expected a not-applicable rate of 0.4, got %v", s.NotApplicableRate)
	}
}

func TestSignalsSplitByCommitSoRegressionsAreVisible(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	older := time.Now().Add(-48 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)

	// The old commit behaved; the new one contradicts reality.
	for i := 0; i < 4; i++ {
		r := report("old-"+string(rune('a'+i)), "agent-1", "s",
			SkillOutcome{SkillName: "deploy", SkillCommit: "old111", Verdict: VerdictApplied})
		r.ReportedAt = older
		if _, err := store.RecordReport(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		r := report("new-"+string(rune('a'+i)), "agent-1", "s",
			SkillOutcome{SkillName: "deploy", SkillCommit: "new222", Verdict: VerdictContradicted})
		r.ReportedAt = newer
		if _, err := store.RecordReport(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	signals, err := store.ListSignals(ctx, "deploy", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 2 {
		t.Fatalf("expected one row per commit, got %d", len(signals))
	}
	// Ordered by first observation, so a rising rate reads top to bottom.
	if signals[0].SkillCommit != "old111" || signals[1].SkillCommit != "new222" {
		t.Fatalf("expected commits ordered by first observation, got %q then %q",
			signals[0].SkillCommit, signals[1].SkillCommit)
	}
	if signals[0].DefectRate != 0 || signals[1].DefectRate != 1 {
		t.Fatalf("expected the defect rate to jump from 0 to 1 across the commits, got %v then %v",
			signals[0].DefectRate, signals[1].DefectRate)
	}
}

func TestMinReportedSessionsSuppressesThinRows(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.RecordReport(ctx, report("r1", "agent-1", "s1",
		SkillOutcome{SkillName: "deploy", SkillCommit: "abc", Verdict: VerdictApplied}))
	if err != nil {
		t.Fatal(err)
	}

	signals, err := store.ListSignals(ctx, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 0 {
		t.Fatalf("expected a single-session row to be suppressed at min 5, got %+v", signals)
	}
}

func TestRollupPreservesCountsAndDropsRawRows(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	old := time.Now().Add(-100 * 24 * time.Hour)
	for i := 0; i < 3; i++ {
		r := report("old-"+string(rune('a'+i)), "agent-1", "s",
			SkillOutcome{SkillName: "deploy", SkillCommit: "abc", Verdict: VerdictContradicted, Note: "broke"})
		r.ReportedAt = old
		if _, err := store.RecordReport(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// One recent report that must survive untouched.
	if _, err := store.RecordReport(ctx, report("recent", "agent-1", "s",
		SkillOutcome{SkillName: "deploy", SkillCommit: "abc", Verdict: VerdictApplied})); err != nil {
		t.Fatal(err)
	}

	before, err := store.ListSignals(ctx, "deploy", 0)
	if err != nil {
		t.Fatal(err)
	}

	n, err := store.Rollup(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("expected 3 aged reports to be rolled up, got %d", n)
	}

	after, err := store.ListSignals(ctx, "deploy", 0)
	if err != nil {
		t.Fatal(err)
	}
	// The aggregate a caller sees must be identical across a rollup - that
	// is the property that makes retention safe to run unattended.
	if len(after) != 1 || after[0].ReportedSessions != before[0].ReportedSessions {
		t.Fatalf("rollup changed the visible signal: %+v vs %+v", before, after)
	}
	if after[0].DefectRate != before[0].DefectRate {
		t.Fatalf("rollup changed the defect rate: %v vs %v", before[0].DefectRate, after[0].DefectRate)
	}

	// The raw rows behind the aged reports are gone, notes and all.
	reports, err := store.ListReports(ctx, ReportFilter{SkillName: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].ReportID != "recent" {
		t.Fatalf("expected only the recent raw report to survive, got %+v", reports)
	}
}

func TestRollupIsCumulativeAcrossRuns(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	add := func(id string, at time.Time) {
		r := report(id, "agent-1", "s",
			SkillOutcome{SkillName: "deploy", SkillCommit: "abc", Verdict: VerdictApplied})
		r.ReportedAt = at
		if _, err := store.RecordReport(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	add("a", time.Now().Add(-200*24*time.Hour))
	if _, err := store.Rollup(ctx, time.Now().Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	add("b", time.Now().Add(-100*24*time.Hour))
	if _, err := store.Rollup(ctx, time.Now().Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	signals, err := store.ListSignals(ctx, "deploy", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 || signals[0].ReportedSessions != 2 {
		t.Fatalf("expected a second rollup to add to the first, got %+v", signals)
	}
}

func TestListReportsFiltersToWhatAProposerNeeds(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	mustRecord := func(id string, v Verdict, note string) {
		if _, err := store.RecordReport(ctx, report(id, "agent-"+id, "session-"+id,
			SkillOutcome{SkillName: "deploy", SkillCommit: "abc", Verdict: v, Note: note})); err != nil {
			t.Fatal(err)
		}
	}
	mustRecord("1", VerdictApplied, "")
	mustRecord("2", VerdictContradicted, "migrate up fails without --baseline")
	mustRecord("3", VerdictContradicted, "same here")

	got, err := store.ListReports(ctx, ReportFilter{
		SkillName:         "deploy",
		Verdict:           VerdictContradicted,
		FilterVerdict:     true,
		ExcludeEmptyNotes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected the 2 contradicted reports with notes, got %d", len(got))
	}
	for _, r := range got {
		if r.Verdict != VerdictContradicted || r.Note == "" {
			t.Fatalf("filter leaked an unwanted row: %+v", r)
		}
	}
}
