// Package evidence stores agent outcome reports and aggregates them into
// per-skill, per-commit signals.
//
// This is the one piece of skillset that is not derived from git.
//
// A skill's content, a proposal, its endorsements, its submission - all of
// those are recomputable from the repository, and losing this component's
// volume costs nothing but a re-clone. Outcome reports are different: they
// are primary observations of how a skill behaved in the field, they exist
// nowhere else, and nothing can reconstruct them. Deploy the volume
// accordingly, and see Store.Backup.
//
// The storage engine is SQLite.
package evidence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: the binaries build CGO_ENABLED=0
)

// Verdict is how one skill fared in one session. The values mirror
// skills.v1.Verdict; the store keeps its own type so the schema doesn't
// shift underneath it if the proto's numbering ever changes.
type Verdict int

const (
	VerdictUnspecified Verdict = iota
	VerdictApplied
	VerdictAppliedWithCorrection
	VerdictContradicted
	VerdictIncomplete
	VerdictNotApplicable
)

// isDefect reports whether a verdict implies the skill's *content* is
// wrong, stale, or incomplete. NOT_APPLICABLE is excluded on purpose: it
// indicts the frontmatter description that caused the skill to be loaded,
// not the body, and folding the two together would send agents to fix the
// wrong thing.
func (v Verdict) isDefect() bool {
	switch v {
	case VerdictAppliedWithCorrection, VerdictContradicted, VerdictIncomplete:
		return true
	default:
		return false
	}
}

// SkillOutcome is how one skill fared within one reported session.
type SkillOutcome struct {
	SkillName   string
	SkillCommit string
	Verdict     Verdict
	Note        string
}

// Report is one agent session's outcome for every skill it used.
type Report struct {
	ReportID   string
	AgentID    string
	SessionID  string
	ReportedAt time.Time
	Skills     []SkillOutcome
}

// StoredOutcome is a single (session, skill) observation read back out.
type StoredOutcome struct {
	ReportID    string
	AgentID     string
	SessionID   string
	SkillName   string
	SkillCommit string
	Verdict     Verdict
	Note        string
	ReportedAt  time.Time
}

// Signal is the aggregate for one (skill, commit) pair.
type Signal struct {
	SkillName         string
	SkillCommit       string
	ReportedSessions  int64
	VerdictCounts     map[Verdict]int64
	DefectRate        float64
	NotApplicableRate float64
	FirstReportedAt   time.Time
	LastReportedAt    time.Time
}

// ReportFilter narrows a ListReports query.
type ReportFilter struct {
	SkillName         string
	SkillCommit       string // Optional
	Verdict           Verdict
	FilterVerdict     bool
	ExcludeEmptyNotes bool
	Limit             int
}

// Store is the evidence database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS report (
    report_id   TEXT PRIMARY KEY,
    agent_id    TEXT NOT NULL,
    session_id  TEXT NOT NULL,
    reported_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS report_skill (
    report_id    TEXT NOT NULL,
    skill_name   TEXT NOT NULL,
    skill_commit TEXT NOT NULL,
    verdict      INTEGER NOT NULL,
    note         TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (report_id, skill_name)
);

CREATE INDEX IF NOT EXISTS report_skill_by_skill
    ON report_skill (skill_name, skill_commit);

-- signal_rollup holds aggregates for reports old enough to have been aged
-- out of the two tables above. Retention is not optional here: raw reports
-- grow without bound and nobody notices until the volume fills, so Rollup
-- folds them into counts on a timer. The counts survive; the notes and the
-- individual report IDs do not.
CREATE TABLE IF NOT EXISTS signal_rollup (
    skill_name        TEXT NOT NULL,
    skill_commit      TEXT NOT NULL,
    verdict           INTEGER NOT NULL,
    sessions          INTEGER NOT NULL,
    first_reported_at INTEGER NOT NULL,
    last_reported_at  INTEGER NOT NULL,
    PRIMARY KEY (skill_name, skill_commit, verdict)
);
`

// Open opens (creating if needed) the evidence database at path.
//
// WAL mode lets the aggregate queries read while a report is being written;
// with one writer process that is all the concurrency control needed. If
// this ever has to serve the read fleet directly, or sustain more than a
// few hundred writes a second, that is the point to move to Postgres.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("evidence: opening %s: %w", path, err)
	}

	// One writer, serialized. SQLite would return SQLITE_BUSY under
	// concurrent writes from a pool; capping the pool at one connection
	// makes the serialization explicit rather than a retry loop.
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("evidence: applying schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// RecordReport stores one session's outcomes, returning false if this
// report_id was already present.
//
// Idempotency is the whole point of the caller-supplied report ID: the
// registry is a single replica, so a rolling restart will drop some
// in-flight calls, and an agent must be able to retry without inflating the
// counts it is retrying into.
func (s *Store) RecordReport(ctx context.Context, r Report) (bool, error) {
	if r.ReportID == "" {
		return false, errors.New("evidence: report_id is required")
	}
	if r.AgentID == "" {
		return false, errors.New("evidence: agent_id is required")
	}
	if r.SessionID == "" {
		return false, errors.New("evidence: session_id is required")
	}
	if len(r.Skills) == 0 {
		return false, errors.New("evidence: at least one skill outcome is required")
	}
	for _, o := range r.Skills {
		if o.SkillName == "" {
			return false, errors.New("evidence: skill_name is required for every outcome")
		}
		if o.SkillCommit == "" {
			return false, fmt.Errorf("evidence: skill_commit is required for %q; "+
				"take it from SkillMetadata.commit, an outcome that doesn't name a version can't "+
				"distinguish a regression from a long-standing defect", o.SkillName)
		}
		if o.Verdict == VerdictUnspecified {
			return false, fmt.Errorf("evidence: verdict is required for %q", o.SkillName)
		}
	}

	if r.ReportedAt.IsZero() {
		r.ReportedAt = time.Now()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("evidence: beginning transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO report (report_id, agent_id, session_id, reported_at)
		 VALUES (?, ?, ?, ?) ON CONFLICT (report_id) DO NOTHING`,
		r.ReportID, r.AgentID, r.SessionID, r.ReportedAt.Unix())
	if err != nil {
		return false, fmt.Errorf("evidence: inserting report: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return false, nil // idempotent replay
	}

	for _, o := range r.Skills {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO report_skill (report_id, skill_name, skill_commit, verdict, note)
			 VALUES (?, ?, ?, ?, ?) ON CONFLICT (report_id, skill_name) DO NOTHING`,
			r.ReportID, o.SkillName, o.SkillCommit, int(o.Verdict), o.Note)
		if err != nil {
			return false, fmt.Errorf("evidence: inserting outcome for %q: %w", o.SkillName, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("evidence: committing report: %w", err)
	}
	return true, nil
}

// listSignalsQuery unions live reports with aged-out rollups so a signal
// reads the same before and after retention has run over it.
const listSignalsQuery = `
SELECT skill_name, skill_commit, verdict,
       SUM(sessions), MIN(first_at), MAX(last_at)
FROM (
    SELECT rs.skill_name, rs.skill_commit, rs.verdict,
           COUNT(*) AS sessions,
           MIN(r.reported_at) AS first_at,
           MAX(r.reported_at) AS last_at
    FROM report_skill rs
    JOIN report r ON r.report_id = rs.report_id
    GROUP BY rs.skill_name, rs.skill_commit, rs.verdict

    UNION ALL

    SELECT skill_name, skill_commit, verdict,
           sessions, first_reported_at, last_reported_at
    FROM signal_rollup
)
GROUP BY skill_name, skill_commit, verdict
`

// ListSignals aggregates reports into one Signal per (skill, commit),
// optionally filtered to one skill and to rows with at least
// minReportedSessions behind them.
//
// Results are ordered by skill name, then by first observation, so
// successive commits of one skill read in the order they were seen and a
// rising defect rate is visible without further work.
func (s *Store) ListSignals(ctx context.Context, skillName string, minReportedSessions int) ([]Signal, error) {
	query := listSignalsQuery
	var args []any
	if skillName != "" {
		query += " HAVING skill_name = ?"
		args = append(args, skillName)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("evidence: querying signals: %w", err)
	}
	defer rows.Close()

	type key struct{ name, commit string }
	byKey := make(map[key]*Signal)
	for rows.Next() {
		var k key
		var verdict int
		var count, firstAt, lastAt int64
		if err := rows.Scan(&k.name, &k.commit, &verdict, &count, &firstAt, &lastAt); err != nil {
			return nil, fmt.Errorf("evidence: scanning signal row: %w", err)
		}

		sig, ok := byKey[k]
		if !ok {
			sig = &Signal{
				SkillName:       k.name,
				SkillCommit:     k.commit,
				VerdictCounts:   make(map[Verdict]int64),
				FirstReportedAt: time.Unix(firstAt, 0).UTC(),
				LastReportedAt:  time.Unix(lastAt, 0).UTC(),
			}
			byKey[k] = sig
		}

		sig.VerdictCounts[Verdict(verdict)] += count
		sig.ReportedSessions += count
		if t := time.Unix(firstAt, 0).UTC(); t.Before(sig.FirstReportedAt) {
			sig.FirstReportedAt = t
		}
		if t := time.Unix(lastAt, 0).UTC(); t.After(sig.LastReportedAt) {
			sig.LastReportedAt = t
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("evidence: reading signal rows: %w", err)
	}

	out := make([]Signal, 0, len(byKey))
	for _, sig := range byKey {
		if minReportedSessions > 0 && sig.ReportedSessions < int64(minReportedSessions) {
			continue
		}
		var defects, notApplicable int64
		for v, n := range sig.VerdictCounts {
			if v.isDefect() {
				defects += n
			}
			if v == VerdictNotApplicable {
				notApplicable += n
			}
		}
		if sig.ReportedSessions > 0 {
			sig.DefectRate = float64(defects) / float64(sig.ReportedSessions)
			sig.NotApplicableRate = float64(notApplicable) / float64(sig.ReportedSessions)
		}
		out = append(out, *sig)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].SkillName != out[j].SkillName {
			return out[i].SkillName < out[j].SkillName
		}
		return out[i].FirstReportedAt.Before(out[j].FirstReportedAt)
	})
	return out, nil
}

// ListReports returns the individual observations behind a signal,
// most-recent-first, so an agent about to propose a fix can read what
// actually went wrong instead of inferring it from a rate.
func (s *Store) ListReports(ctx context.Context, f ReportFilter) ([]StoredOutcome, error) {
	query := `
		SELECT rs.report_id, r.agent_id, r.session_id, rs.skill_name,
		       rs.skill_commit, rs.verdict, rs.note, r.reported_at
		FROM report_skill rs
		JOIN report r ON r.report_id = rs.report_id
		WHERE rs.skill_name = ?`
	args := []any{f.SkillName}

	if f.SkillCommit != "" {
		query += " AND rs.skill_commit = ?"
		args = append(args, f.SkillCommit)
	}
	if f.FilterVerdict && f.Verdict != VerdictUnspecified {
		query += " AND rs.verdict = ?"
		args = append(args, int(f.Verdict))
	}
	if f.ExcludeEmptyNotes {
		query += " AND rs.note != ''"
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += " ORDER BY r.reported_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("evidence: querying reports: %w", err)
	}
	defer rows.Close()

	var out []StoredOutcome
	for rows.Next() {
		var o StoredOutcome
		var verdict int
		var reportedAt int64
		if err := rows.Scan(&o.ReportID, &o.AgentID, &o.SessionID, &o.SkillName,
			&o.SkillCommit, &verdict, &o.Note, &reportedAt); err != nil {
			return nil, fmt.Errorf("evidence: scanning report row: %w", err)
		}
		o.Verdict = Verdict(verdict)
		o.ReportedAt = time.Unix(reportedAt, 0).UTC()
		out = append(out, o)
	}
	return out, rows.Err()
}

// Rollup folds every report older than cutoff into signal_rollup and
// deletes the raw rows, returning how many reports were collapsed.
//
// This is lossy by design: the aggregate counts survive forever, the notes
// and individual report IDs do not. Keeping raw rows indefinitely would
// grow the volume without bound for the sake of prose nobody reads after
// the proposal it motivated has merged.
func (s *Store) Rollup(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("evidence: beginning rollup: %w", err)
	}
	defer tx.Rollback()

	unix := cutoff.Unix()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO signal_rollup (skill_name, skill_commit, verdict, sessions,
		                           first_reported_at, last_reported_at)
		SELECT rs.skill_name, rs.skill_commit, rs.verdict, COUNT(*),
		       MIN(r.reported_at), MAX(r.reported_at)
		FROM report_skill rs
		JOIN report r ON r.report_id = rs.report_id
		WHERE r.reported_at < ?
		GROUP BY rs.skill_name, rs.skill_commit, rs.verdict
		ON CONFLICT (skill_name, skill_commit, verdict) DO UPDATE SET
		    sessions          = sessions + excluded.sessions,
		    first_reported_at = MIN(first_reported_at, excluded.first_reported_at),
		    last_reported_at  = MAX(last_reported_at, excluded.last_reported_at)`, unix)
	if err != nil {
		return 0, fmt.Errorf("evidence: aggregating aged reports: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM report_skill
		WHERE report_id IN (SELECT report_id FROM report WHERE reported_at < ?)`, unix); err != nil {
		return 0, fmt.Errorf("evidence: deleting aged outcomes: %w", err)
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM report WHERE reported_at < ?`, unix)
	if err != nil {
		return 0, fmt.Errorf("evidence: deleting aged reports: %w", err)
	}
	n, _ := res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("evidence: committing rollup: %w", err)
	}
	return n, nil
}

// Backup writes a consistent snapshot of the database to path, which must
// not already exist.
//
// Unlike the git-backed halves of skillset, nothing here can be
// reconstructed from upstream, so this is the only thing standing between a
// lost volume and a permanently lost observation history.
func (s *Store) Backup(ctx context.Context, path string) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("evidence: backing up to %s: %w", path, err)
	}
	return nil
}
