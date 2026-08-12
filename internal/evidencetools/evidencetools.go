// Package evidencetools registers skillsd-registry's evidence MCP tools:
// reporting how a skill fared in a session, and reading the aggregated
// signal back.
//
// Add is called only when evidence collection is enabled, so these tools
// are simply absent from tools/list when it is off rather than registered
// and failing. A tool that isn't there is the more direct signal: an agent
// sees no report_outcome at all instead of discovering it errors.
package evidencetools

import (
	"context"
	"fmt"
	"time"

	"github.com/araghukas/skillset/internal/evidence"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultReportLimit = 100
	maxReportLimit     = 500
)

// SkillResolver checks that a reported (skill, commit) pair actually
// existed. It's satisfied by proposals.Service, which already has the
// repository open.
type SkillResolver interface {
	SkillExistsAt(ctx context.Context, skillName, commit string) error
}

// Deps is what the evidence tools need to run.
type Deps struct {
	Store    *evidence.Store
	Resolver SkillResolver

	// Verify, when true, rejects reports naming a skill/commit pair the
	// registry's repository doesn't contain. This is the main defense
	// against a misconfigured agent quietly filling the store with rows
	// that look like signal, and it costs one tree lookup per reported
	// skill.
	Verify bool
}

// Add registers the evidence tools on srv.
func Add(srv *mcp.Server, deps Deps) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "report_outcome",
		Description: "Report how one or more skills fared in a session you just completed. " +
			"Report every skill you used, including the ones that worked - omitting those " +
			"inflates every defect rate this server computes. Safe to retry with the same " +
			"report_id: a repeat is accepted and changes nothing.",
		Annotations: writeTool(idempotent()),
		InputSchema: withVerdictEnum[ReportOutcomeInput]("skills"),
	}, reportOutcome(deps))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_skill_signals",
		Description: "Read the aggregated outcome signal for skills: how many sessions " +
			"reported using each (skill, commit) pair, and how they turned out. This is the " +
			"\"what should I fix next\" query. reported_sessions counts only sessions that " +
			"reported - a session that crashed never reports - so treat every rate here as " +
			"\"among sessions that reported\", not \"among sessions\".",
		Annotations: readOnly(),
	}, listSkillSignals(deps))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_outcome_reports",
		Description: "Read the individual reports behind a signal, so you can see what " +
			"actually went wrong before proposing a fix. Cite the report_id of relevant " +
			"reports in propose_change's motivating_report_ids.",
		Annotations: readOnly(),
		InputSchema: withVerdictEnum[ListOutcomeReportsInput]("verdict"),
	}, listOutcomeReports(deps))
}

// withVerdictEnum infers T's schema, then constrains the verdict field(s)
// at the given paths to evidence.VerdictNames(). The struct tag
// jsonschema-go reads carries a description, not a value list; this is the
// one piece of the schema built by hand rather than inferred, so a model
// sees the exact set of valid verdicts in tools/list rather than
// discovering it from a rejected call. It also means an invalid verdict is
// rejected by schema validation before the handler runs at all - the
// handler's own evidence.ParseVerdict call is a second line of defense for
// any caller that somehow gets past the schema.
//
// A path of "skills" reaches into the items of an array property named
// "skills" (as in ReportOutcomeInput.Skills); anything else names a
// top-level property directly.
func withVerdictEnum[T any](paths ...string) *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("evidencetools: inferring schema for %T: %v", *new(T), err))
	}
	for _, path := range paths {
		prop, ok := schema.Properties[path]
		if !ok {
			panic(fmt.Sprintf("evidencetools: %T has no property %q to constrain", *new(T), path))
		}
		target := prop
		if prop.Items != nil {
			target = prop.Items
		}
		verdictProp, ok := target.Properties["verdict"]
		if ok {
			verdictProp.Enum = verdictEnum()
			continue
		}
		if path == "verdict" {
			prop.Enum = verdictEnum()
			continue
		}
		panic(fmt.Sprintf("evidencetools: could not find a verdict field under %q on %T", path, *new(T)))
	}
	return schema
}

func verdictEnum() []any {
	names := evidence.VerdictNames()
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out
}

func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}
}

type annotationOpt func(*mcp.ToolAnnotations)

func idempotent() annotationOpt {
	return func(a *mcp.ToolAnnotations) { a.IdempotentHint = true }
}

func writeTool(opts ...annotationOpt) *mcp.ToolAnnotations {
	a := &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		OpenWorldHint:   new(false),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// SkillOutcomeInput is how one skill fared within the reported session.
type SkillOutcomeInput struct {
	SkillName string `json:"skill_name"`

	// SkillCommit is required so an outcome can be attributed to a
	// specific version - a report that doesn't say which version it is
	// about can't distinguish a fix from a regression.
	SkillCommit string `json:"skill_commit" jsonschema:"the commit field from the skill's metadata, as returned by get_skill"`

	Verdict string `json:"verdict" jsonschema:"one of: applied, applied_with_correction, contradicted, incomplete, not_applicable"`

	// Note is ignored for the "applied" verdict.
	Note string `json:"note,omitempty" jsonschema:"what specifically went wrong - the command that failed, the instruction that was wrong. A reviewer reads this, so be concrete. Ignored for the applied verdict"`
}

// ReportOutcomeInput is one session's outcome for every skill it used.
type ReportOutcomeInput struct {
	// ReportID makes the call idempotent: generate a UUID once, before the
	// first attempt, and reuse it on every retry.
	ReportID string `json:"report_id" jsonschema:"a UUID you generate once, before the first attempt; reuse it on every retry so a retry does not double-count"`

	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id" jsonschema:"identifies your session; one report per session"`

	Skills []SkillOutcomeInput `json:"skills" jsonschema:"every skill this session used, including the ones that worked"`
}

// ReportOutcomeOutput reports whether the call recorded anything new.
type ReportOutcomeOutput struct {
	// Recorded is false if report_id had already been stored, meaning this
	// call was an idempotent replay and nothing changed. Not an error.
	Recorded bool `json:"recorded"`
}

func reportOutcome(deps Deps) mcp.ToolHandlerFor[ReportOutcomeInput, ReportOutcomeOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ReportOutcomeInput) (*mcp.CallToolResult, ReportOutcomeOutput, error) {
		report := evidence.Report{
			ReportID:  in.ReportID,
			AgentID:   in.AgentID,
			SessionID: in.SessionID,
		}
		for _, o := range in.Skills {
			v, err := evidence.ParseVerdict(o.Verdict)
			if err != nil {
				return nil, ReportOutcomeOutput{}, fmt.Errorf("skill %q: %w", o.SkillName, err)
			}
			report.Skills = append(report.Skills, evidence.SkillOutcome{
				SkillName:   o.SkillName,
				SkillCommit: o.SkillCommit,
				Verdict:     v,
				Note:        o.Note,
			})
		}

		if deps.Verify {
			for _, o := range report.Skills {
				if err := deps.Resolver.SkillExistsAt(ctx, o.SkillName, o.SkillCommit); err != nil {
					// The commonest cause is a commit newer than this
					// registry's last fetch of the base branch, which
					// resolves on its own within a fetch interval. The
					// report ID makes retrying free, so the right advice is
					// to retry rather than to drop the observation.
					return nil, ReportOutcomeOutput{}, fmt.Errorf(
						"cannot attribute outcome to %s@%s: %w; if that commit is newer than this "+
							"registry's last fetch, retry with the same report_id shortly",
						o.SkillName, o.SkillCommit, err)
				}
			}
		}

		recorded, err := deps.Store.RecordReport(ctx, report)
		if err != nil {
			return nil, ReportOutcomeOutput{}, err
		}
		return nil, ReportOutcomeOutput{Recorded: recorded}, nil
	}
}

// ListSkillSignalsInput filters the signal listing.
type ListSkillSignalsInput struct {
	SkillName string `json:"skill_name,omitempty" jsonschema:"only signals for this skill; omit for all skills"`

	// MinReportedSessions suppresses rows built on too little data to read
	// anything into.
	MinReportedSessions int `json:"min_reported_sessions,omitempty" jsonschema:"suppress rows with fewer than this many reported sessions; omit for 1"`
}

// VerdictCountOutput is how many reported sessions landed on one verdict.
type VerdictCountOutput struct {
	Verdict string `json:"verdict"`
	Count   int64  `json:"count"`
}

// SkillSignalOutput is the aggregate for one (skill, commit) pair.
type SkillSignalOutput struct {
	SkillName   string `json:"skill_name"`
	SkillCommit string `json:"skill_commit"`

	// ReportedSessions is the number of distinct sessions that reported
	// using this skill at this commit - not the number that used it.
	// Reporting is voluntary, so every rate below is "among sessions that
	// reported", never "among sessions".
	ReportedSessions int64 `json:"reported_sessions"`

	VerdictCounts []VerdictCountOutput `json:"verdict_counts"`

	// DefectRate is the share of reported sessions with a verdict implying
	// the skill's content is wrong, stale, or incomplete.
	DefectRate float64 `json:"defect_rate"`

	// NotApplicableRate is tracked separately from DefectRate because it
	// implies a different repair: the skill body may be fine and its
	// frontmatter description miscalibrated instead.
	NotApplicableRate float64   `json:"not_applicable_rate"`
	FirstReportedAt   time.Time `json:"first_reported_at,omitzero"`
	LastReportedAt    time.Time `json:"last_reported_at,omitzero"`
}

// ListSkillSignalsOutput is the signal listing.
type ListSkillSignalsOutput struct {
	// Signals are sorted by skill name, then by first_reported_at
	// ascending, so successive commits of one skill read in the order they
	// were observed and a rising defect_rate is visible by eye.
	Signals []SkillSignalOutput `json:"signals"`
}

func listSkillSignals(deps Deps) mcp.ToolHandlerFor[ListSkillSignalsInput, ListSkillSignalsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListSkillSignalsInput) (*mcp.CallToolResult, ListSkillSignalsOutput, error) {
		min := in.MinReportedSessions
		if min <= 0 {
			min = 1
		}
		signals, err := deps.Store.ListSignals(ctx, in.SkillName, min)
		if err != nil {
			return nil, ListSkillSignalsOutput{}, err
		}

		out := ListSkillSignalsOutput{Signals: make([]SkillSignalOutput, 0, len(signals))}
		for _, sig := range signals {
			counts := make([]VerdictCountOutput, 0, len(sig.VerdictCounts))
			// Iterate the fixed order rather than the map, so a signal
			// serializes identically on every call.
			for _, v := range evidence.Verdicts {
				if n, ok := sig.VerdictCounts[v]; ok {
					counts = append(counts, VerdictCountOutput{Verdict: v.String(), Count: n})
				}
			}
			out.Signals = append(out.Signals, SkillSignalOutput{
				SkillName:         sig.SkillName,
				SkillCommit:       sig.SkillCommit,
				ReportedSessions:  sig.ReportedSessions,
				VerdictCounts:     counts,
				DefectRate:        sig.DefectRate,
				NotApplicableRate: sig.NotApplicableRate,
				FirstReportedAt:   sig.FirstReportedAt,
				LastReportedAt:    sig.LastReportedAt,
			})
		}
		return nil, out, nil
	}
}

// ListOutcomeReportsInput filters the report listing.
type ListOutcomeReportsInput struct {
	SkillName         string `json:"skill_name"`
	SkillCommit       string `json:"skill_commit,omitempty" jsonschema:"only reports for this commit; omit for all commits"`
	Verdict           string `json:"verdict,omitempty" jsonschema:"only reports with this verdict: applied, applied_with_correction, contradicted, incomplete, not_applicable; omit for all verdicts"`
	ExcludeEmptyNotes bool   `json:"exclude_empty_notes,omitempty" jsonschema:"skip reports with no note - usually the applied ones"`
	Limit             int    `json:"limit,omitempty" jsonschema:"maximum reports to return, 1-500; omit for 100"`
}

// OutcomeReportOutput is one stored (session, skill) observation.
type OutcomeReportOutput struct {
	ReportID    string    `json:"report_id"`
	AgentID     string    `json:"agent_id"`
	SessionID   string    `json:"session_id"`
	SkillName   string    `json:"skill_name"`
	SkillCommit string    `json:"skill_commit"`
	Verdict     string    `json:"verdict"`
	Note        string    `json:"note,omitempty"`
	ReportedAt  time.Time `json:"reported_at,omitzero"`
}

// ListOutcomeReportsOutput is the report listing, most-recent-first.
type ListOutcomeReportsOutput struct {
	Reports []OutcomeReportOutput `json:"reports"`
}

func listOutcomeReports(deps Deps) mcp.ToolHandlerFor[ListOutcomeReportsInput, ListOutcomeReportsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListOutcomeReportsInput) (*mcp.CallToolResult, ListOutcomeReportsOutput, error) {
		if in.SkillName == "" {
			return nil, ListOutcomeReportsOutput{}, fmt.Errorf("skill_name is required")
		}

		var verdict evidence.Verdict
		var filterVerdict bool
		if in.Verdict != "" {
			v, err := evidence.ParseVerdict(in.Verdict)
			if err != nil {
				return nil, ListOutcomeReportsOutput{}, err
			}
			verdict, filterVerdict = v, true
		}

		limit := in.Limit
		switch {
		case limit <= 0:
			limit = defaultReportLimit
		case limit > maxReportLimit:
			limit = maxReportLimit
		}

		reports, err := deps.Store.ListReports(ctx, evidence.ReportFilter{
			SkillName:         in.SkillName,
			SkillCommit:       in.SkillCommit,
			Verdict:           verdict,
			FilterVerdict:     filterVerdict,
			ExcludeEmptyNotes: in.ExcludeEmptyNotes,
			Limit:             limit,
		})
		if err != nil {
			return nil, ListOutcomeReportsOutput{}, err
		}

		out := ListOutcomeReportsOutput{Reports: make([]OutcomeReportOutput, 0, len(reports))}
		for _, r := range reports {
			out.Reports = append(out.Reports, OutcomeReportOutput{
				ReportID:    r.ReportID,
				AgentID:     r.AgentID,
				SessionID:   r.SessionID,
				SkillName:   r.SkillName,
				SkillCommit: r.SkillCommit,
				Verdict:     r.Verdict.String(),
				Note:        truncateNote(r.Note),
				ReportedAt:  r.ReportedAt,
			})
		}
		return nil, out, nil
	}
}

// maxNoteBytes bounds one report's note. Notes are free text a reviewer
// reads, and a listing of a hundred reports should not be able to smuggle
// in a hundred essays.
const maxNoteBytes = 2 << 10 // 2 KiB

func truncateNote(note string) string {
	if len(note) <= maxNoteBytes {
		return note
	}
	return note[:maxNoteBytes] + "… (truncated)"
}
