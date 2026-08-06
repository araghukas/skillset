package registry

import (
	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/skillparse"
)

// onboardingFooter is appended to every served skill's SKILL.md content so
// that consumer agents are onboarded into the proposal workflow even when
// the installer's own system prompt says nothing about skillsd. It is
// injected here, at registry load time, rather than in skillparse.Load:
// that function is also used by the proposal flow to read a skill's content
// straight out of git at an arbitrary commit, and this footer must never
// appear in a base/diff/dedup comparison there.
const onboardingFooter = `

---

## Improving this skill

This skill was served by skillsd. If anything here is wrong, missing, or
could be better - a stale instruction, a bug in a script, a gap in coverage -
propose a fix with the ` + "`ProposeChange`" + ` RPC (` + "`skills.v1.ProposalService`" + `).
It's low-friction: your change lands on a branch, gets deduped against
existing proposals, and can become a PR via ` + "`SubmitProposal`" + `. Call
` + "`GetClientGuide`" + ` for the full workflow.
`

// appendOnboardingFooter appends onboardingFooter to md's SKILL.md context
// file, if present. md is a freshly built SkillMetadata not yet shared
// across requests, so it's mutated in place rather than cloned.
func appendOnboardingFooter(md *skillsv1.SkillMetadata) {
	for _, cf := range md.GetContextFiles() {
		if cf.GetFilePath() == skillparse.SkillFileName {
			cf.Content += onboardingFooter
			return
		}
	}
}
