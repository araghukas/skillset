package toollog

import "encoding/json"

// toolFields describes how to pull extra structured fields out of one
// tool's request arguments and response, on top of the tool name and
// duration every call already logs. Either half may be nil: a tool with
// nothing worth singling out (its output is the same shape every time, or
// its arguments are already implied by the tool name) is simply absent
// from extractors and gets the generic log line.
type toolFields struct {
	request  func(args json.RawMessage) []any
	response func(result json.RawMessage) []any
}

// unmarshalFields decodes raw JSON into T and hands it to fn, returning the
// slog key/value pairs fn derives from it. A decode failure (or absent
// JSON) yields no extra fields rather than breaking the log line - logging
// must never be the reason a tool call fails.
// This function wraps all tool-specific request/response functions
// to provide uniform error handling.
func unmarshalFields[T any](fn func(T) []any) func(json.RawMessage) []any {
	return func(raw json.RawMessage) []any {
		if len(raw) == 0 {
			return nil
		}
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil
		}
		return fn(v)
	}
}

// extractors maps a tool name to the fields worth logging about its calls,
// beyond name and duration. Field names here mirror the JSON tags on the
// tool's input/output structs (see skilltools, suggestiontools,
// evidencetools) rather than the Go field names.
var extractors = map[string]toolFields{
	"get_skill": {
		request: unmarshalFields(func(in struct {
			SkillName string `json:"skill_name"`
		}) []any {
			return []any{"skill", in.SkillName}
		}),
	},

	"get_skill_at_ref": {
		request: unmarshalFields(func(in struct {
			SkillName string `json:"skill_name"`
			Ref       string `json:"ref"`
		}) []any {
			return []any{"skill", in.SkillName, "ref", in.Ref}
		}),
	},

	"record_suggestion": {
		request: unmarshalFields(func(in struct {
			SkillName     string `json:"skill_name"`
			AgentID       string `json:"agent_id"`
			CommitMessage string `json:"commit_message"`
		}) []any {
			return []any{"skill", in.SkillName, "agent", in.AgentID, "commit_message", in.CommitMessage}
		}),
		response: unmarshalFields(func(out struct {
			Deduplicated  bool `json:"deduplicated"`
			AutoSubmitted any  `json:"auto_submitted"`
		}) []any {
			return []any{"deduplicated", out.Deduplicated, "auto_submitted", out.AutoSubmitted != nil}
		}),
	},

	"get_suggestion": {
		request: unmarshalFields(func(in struct {
			Branch string `json:"branch"`
		}) []any {
			return []any{"branch", in.Branch}
		}),
	},

	"list_suggestions": {
		request: unmarshalFields(func(in struct {
			SkillName string `json:"skill_name"`
			AgentID   string `json:"agent_id"`
		}) []any {
			return []any{"skill", in.SkillName, "agent", in.AgentID}
		}),
		response: unmarshalFields(func(out struct {
			Total int `json:"total"`
		}) []any {
			return []any{"total", out.Total}
		}),
	},

	"list_suggestion_clusters": {
		request: unmarshalFields(func(in struct {
			SkillName string `json:"skill_name"`
		}) []any {
			return []any{"skill", in.SkillName}
		}),
		response: unmarshalFields(func(out struct {
			Total int `json:"total"`
		}) []any {
			return []any{"total", out.Total}
		}),
	},

	"report_outcome": {
		request: unmarshalFields(func(in struct {
			AgentID string `json:"agent_id"`
			Skills  []struct {
				SkillName string `json:"skill_name"`
				Verdict   string `json:"verdict"`
			} `json:"skills"`
		}) []any {
			skills := make([]string, len(in.Skills))
			verdicts := make([]string, len(in.Skills))
			for i, s := range in.Skills {
				skills[i] = s.SkillName
				verdicts[i] = s.Verdict
			}
			return []any{"agent", in.AgentID, "skills", skills, "verdicts", verdicts}
		}),
		response: unmarshalFields(func(out struct {
			Recorded bool `json:"recorded"`
		}) []any {
			return []any{"recorded", out.Recorded}
		}),
	},

	"list_skill_signals": {
		request: unmarshalFields(func(in struct {
			SkillName string `json:"skill_name"`
		}) []any {
			return []any{"skill", in.SkillName}
		}),
	},

	"list_outcome_reports": {
		request: unmarshalFields(func(in struct {
			SkillName string `json:"skill_name"`
			Verdict   string `json:"verdict"`
		}) []any {
			return []any{"skill", in.SkillName, "verdict", in.Verdict}
		}),
		response: unmarshalFields(func(out struct {
			Reports []any `json:"reports"`
		}) []any {
			return []any{"total", len(out.Reports)}
		}),
	},
}
