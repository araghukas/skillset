package registry

import (
	"github.com/araghukas/skillset/internal/skill"
	"github.com/araghukas/skillset/internal/skillparse"
)

// onboardingFooter is appended to every served skill's SKILL.md content so
// that consumer agents are onboarded into the proposal workflow even when
// the installer's own system prompt says nothing about skillsd. It is
// injected here, at registry load time, rather than in skillparse.Load:
// that function is also used by the proposal flow to read a skill's content
// straight out of git at an arbitrary commit, and this footer must never
// appear in a base/diff/dedup comparison there.
//
// It names tools rather than describing them, because an agent reading a
// skill body has the tool list in front of it and does not have the
// server's instructions in reach.
const onboardingFooter = `

---

## Improving this skill

This skill was served by skillsd. If anything here is wrong, missing, or
could be better - a stale instruction, a bug in a script, a gap in coverage -
propose a fix with the ` + "`propose_change`" + ` tool on the skillsd-registry
MCP server. It's low-friction: your change lands on a branch, gets deduped
against existing proposals, and can become a pull request via
` + "`submit_proposal`" + `. Call ` + "`get_client_guide`" + ` for the full workflow.
`

// appendOnboardingFooter appends onboardingFooter to md's SKILL.md context
// file, if present. md is a freshly built skill.Metadata not yet shared
// across requests, so it's mutated in place rather than cloned.
func appendOnboardingFooter(md *skill.Metadata) {
	for i := range md.ContextFiles {
		if md.ContextFiles[i].FilePath == skillparse.SkillFileName {
			md.ContextFiles[i].Content += onboardingFooter
			return
		}
	}
}
