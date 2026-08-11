//go:build e2e

// Exercises skillsd's read-path tools against whatever skillsRepo it was
// deployed with. Assumes the seed content from local/gitea-init.sh (a
// private copy of anthropics/skills' skills/ tree, which includes
// "internal-comms") unless SKILL_NAME is overridden.
package verify

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listSkillsResult struct {
	Skills []struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		ContextFiles int    `json:"context_files"`
	} `json:"skills"`
	IndexedAt string `json:"indexed_at"`
	Total     int    `json:"total"`
}

func decodeStructured(t *testing.T, res *mcp.CallToolResult, into any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool call failed: %s", contentText(res))
	}
	if res.StructuredContent == nil {
		t.Fatal("result carries no structured content")
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("unmarshalling structured content: %v\nraw: %s", err, raw)
	}
}

// TestListSkills replaces SkillService.ListSkills. Baseline listing: the
// index loaded, and the seed skill is present.
func TestListSkills(t *testing.T) {
	session := connect(t, skillsdAddr())

	res := callTool(t, session, "list_skills", nil)
	var list listSkillsResult
	decodeStructured(t, res, &list)

	if list.IndexedAt == "" {
		t.Error("response has no indexed_at timestamp")
	}
	if len(list.Skills) == 0 {
		t.Fatal("skills list is empty")
	}

	found := false
	for _, s := range list.Skills {
		if s.Name == skillName() {
			found = true
			if s.ContextFiles == 0 {
				t.Errorf("seed skill %s reports 0 context files", skillName())
			}
			break
		}
	}
	if !found {
		t.Errorf("seed skill %q not present in list_skills", skillName())
	}
}

// TestListSkillsHasNoFileContent pins the behavior change that motivated
// splitting list_skills from get_skill: the old ListSkills(
// include_context_files=true) could return every file of every skill in
// one call - routinely exceeding 4 MiB - which
// would land in a model's context window under MCP. list_skills now
// carries no file-content field at all; only get_skill returns bodies.
func TestListSkillsHasNoFileContent(t *testing.T) {
	session := connect(t, skillsdAddr())
	res := callTool(t, session, "list_skills", nil)
	if strings.Contains(contentText(res), "context_files\":[") {
		t.Error("list_skills output appears to carry file content, not just counts")
	}
}

// TestGetSkill replaces SkillService.GetSkill, checked in detail.
func TestGetSkill(t *testing.T) {
	session := connect(t, skillsdAddr())

	res := callTool(t, session, "get_skill", map[string]any{
		"skill_name":            skillName(),
		"include_context_files": true,
	})
	if res.IsError {
		t.Fatalf("get_skill(%s) failed: %s", skillName(), contentText(res))
	}
	text := contentText(res)

	if !strings.Contains(text, "skill: "+skillName()) {
		t.Errorf("returned skill does not name itself %q: %q", skillName(), firstLine(text))
	}
	if !strings.Contains(text, "description:") {
		t.Error("returned skill has no description line")
	}
	if !strings.Contains(text, "commit:") {
		t.Error("returned skill has no commit line")
	}
	if !strings.Contains(text, "SKILL.md") {
		t.Error("returned skill does not include a SKILL.md context file")
	}
	if !strings.Contains(text, "served by skillsd") {
		t.Error("SKILL.md content does not carry the onboarding footer (\"served by skillsd\")")
	}
}

// TestGetSkillUnknownSkillIsToolError replaces the old negative GetSkill
// case. An unknown skill must arrive as a tool error (IsError, readable
// message), not a protocol-level failure - see mcp.ToolHandlerFor's
// error-routing contract.
func TestGetSkillUnknownSkillIsToolError(t *testing.T) {
	session := connect(t, skillsdAddr())
	res := callTool(t, session, "get_skill", map[string]any{"skill_name": "does-not-exist"})
	if !res.IsError {
		t.Fatal("get_skill on an unknown skill did not set IsError")
	}
	if !strings.Contains(contentText(res), "does-not-exist") {
		t.Errorf("error does not name the missing skill: %q", contentText(res))
	}
}

// TestGetClientGuide replaces SkillService.GetClientGuide. The embedded
// onboarding guide: always available, never listed as a skill, never
// double-stamped with the served-skill onboarding footer that would only
// make sense on a skill agents can read out of the repo.
func TestGetClientGuide(t *testing.T) {
	session := connect(t, skillsdAddr())

	guide := callTool(t, session, "get_client_guide", nil)
	if guide.IsError {
		t.Fatalf("get_client_guide failed: %s", contentText(guide))
	}
	guideText := contentText(guide)
	if len(guideText) == 0 {
		t.Fatal("client guide has no content")
	}

	list := callTool(t, session, "list_skills", nil)
	var parsed listSkillsResult
	decodeStructured(t, list, &parsed)
	for _, s := range parsed.Skills {
		if s.Name == "skillsd-client" {
			t.Error("the client guide is listed as an ordinary skill by list_skills")
		}
	}

	if strings.Contains(guideText, "served by skillsd") {
		t.Error("the client guide itself carries the served-skill onboarding footer (it should not - " +
			"that footer is for skills read out of the repo, not for the guide describing it)")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
