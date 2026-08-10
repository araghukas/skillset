package proposals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"path"
	"sort"
	"strings"

	"github.com/araghukas/skillset/internal/gitrepo"
	"github.com/araghukas/skillset/internal/storage"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	// endorsementRefPrefix and submissionRefPrefix namespace the annotations
	// that hang off a proposal branch. Both sit outside refs/heads and
	// refs/tags so nothing mistakes them for a branch or a release tag, and
	// both are pushed alongside the branch on submission.
	endorsementRefPrefix = "refs/endorsements/"
	submissionRefPrefix  = "refs/submissions/"

	// clusterContextLines is how far apart two edits can be and still count
	// as touching the same region. It matches unified diff's default context:
	// if the two changes would render in one hunk, a reviewer reads them as
	// one contested passage, so the clusterer should too.
	clusterContextLines = 3
)

// contentHashAt returns the normalized content hash of skillName's directory
// as of commit hash.
func (s *Service) contentHashAt(ctx context.Context, skillName string, hash plumbing.Hash) (string, error) {
	files, err := s.skillFilesAt(ctx, skillName, hash)
	if err != nil {
		return "", err
	}
	return hashFiles(files), nil
}

// skillFilesAt reads every file in skillName's directory as of commit hash,
// keyed by full repo-relative path.
func (s *Service) skillFilesAt(ctx context.Context, skillName string, hash plumbing.Hash) (map[string][]byte, error) {
	tree, err := s.repo.Tree(hash)
	if err != nil {
		return nil, err
	}

	backend := storage.NewGitTreeBackend(tree)
	dirPrefix := path.Join(s.subPath, skillName)
	keys, err := backend.List(ctx, dirPrefix)
	if err != nil {
		return nil, fmt.Errorf("proposals: listing %s: %w", dirPrefix, err)
	}

	out := make(map[string][]byte, len(keys))
	for _, key := range keys {
		obj, err := backend.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("proposals: reading %s: %w", key, err)
		}
		out[key] = obj.Content
	}
	return out, nil
}

// hashFiles digests a whole skill directory into one hex string. Paths are
// sorted so map iteration order can't affect the result, and each file's
// content is normalized first, so two agents that produced the same skill
// with different trailing whitespace still land on the same hash.
func hashFiles(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s\x00%x\x00", p, sha256.Sum256(normalizeContent(files[p])))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// normalizeContent canonicalizes a file for hashing: CRLF to LF, no trailing
// whitespace on any line, no blank lines at EOF, exactly one final newline.
// These are differences no reviewer would consider a different proposal, so
// letting them split a cluster would just mean more PRs saying the same thing.
func normalizeContent(b []byte) []byte {
	text := strings.ReplaceAll(string(b), "\r\n", "\n")

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// applyChanges returns what files would look like with the given changes
// applied, without writing anything. ProposeChange uses it to learn a
// prospective content hash *before* committing, so a duplicate proposal can
// be turned into an endorsement without ever creating a branch to abandon.
func applyChanges(files map[string][]byte, subPath, skillName string, changes []FileEdit) map[string][]byte {
	out := make(map[string][]byte, len(files))
	maps.Copy(out, files)

	for _, fc := range changes {
		key := path.Join(subPath, skillName, fc.FilePath)
		if fc.Deleted {
			delete(out, key)
			continue
		}
		out[key] = []byte(fc.Content)
	}
	return out
}

// ==========================================
// Endorsements
// ==========================================

// endorsementRef is where a single agent's endorsement of a proposal lives.
// The branch's own agent/skill/id triple is reused as the path so the ref
// tree mirrors the branch tree and a prefix scan finds every endorsement of
// one proposal.
func endorsementRef(branch, endorserID string) string {
	return endorsementRefPrefix + strings.TrimPrefix(branch, "proposals/") + "/" + endorserID
}

func endorsementRefPrefixFor(branch string) string {
	return endorsementRefPrefix + strings.TrimPrefix(branch, "proposals/") + "/"
}

// Endorse records that endorserID independently produced the content already
// on branch at head.
func (s *Service) Endorse(branch, endorserID string, head plumbing.Hash) error {
	msg := fmt.Sprintf("Independently produced identical content for %s.", branch)
	return s.repo.Annotate(endorsementRef(branch, endorserID), endorserID, msg, head)
}

// endorsementsFor reads a proposal's endorsements and its corroboration
// count: the proposing agent, plus every endorser still pointing at the
// current head.
//
// An endorsement targets the exact commit it was made against. When the
// proposal moves on, earlier endorsements are kept but marked stale and
// excluded from the count - an agent corroborated the content it actually
// arrived at, and carrying that forward onto a later revision it never saw
// would manufacture agreement that never happened.
func (s *Service) endorsementsFor(branch string, head plumbing.Hash) ([]Endorsement, int, error) {
	annotations, err := s.repo.Annotations(endorsementRefPrefixFor(branch))
	if err != nil {
		return nil, 0, err
	}

	corroboration := 1 // the proposing agent
	out := make([]Endorsement, 0, len(annotations))
	for _, a := range annotations {
		stale := a.Target != head.String()
		if !stale {
			corroboration++
		}
		out = append(out, Endorsement{
			AgentID:     a.Author,
			EndorsedSHA: a.Target,
			Stale:       stale,
			EndorsedAt:  a.At,
		})
	}
	return out, corroboration, nil
}

// findDuplicate looks for an open proposal for skillName, by an agent other
// than agentID, whose content hash already equals hash.
func (s *Service) findDuplicate(ctx context.Context, skillName, agentID, hash string) (*Proposal, error) {
	existing, err := s.ListProposals(ctx, skillName, "")
	if err != nil {
		return nil, err
	}
	for _, p := range existing {
		if p.AgentID == agentID {
			continue
		}
		if p.ContentHash == hash {
			return p, nil
		}
	}
	return nil, nil
}

// ==========================================
// Submission markers
// ==========================================

func submissionRef(branch string) string {
	return submissionRefPrefix + strings.TrimPrefix(branch, "proposals/")
}

// MarkSubmitted records that a pull request has been opened for branch.
// Like everything else about a proposal, this lives in git rather than a
// database - it's what stops the auto-submit threshold from opening a second
// pull request for a proposal that already has one.
func (s *Service) MarkSubmitted(branch string, head plumbing.Hash, prURL string, prNumber int64) error {
	msg := fmt.Sprintf("%s\n%d\n", prURL, prNumber)
	return s.repo.Annotate(submissionRef(branch), "skillsd-registry", msg, head)
}

// Submission returns the pull request already opened for branch, if any.
func (s *Service) Submission(branch string) (*Submission, bool, error) {
	annotations, err := s.repo.Annotations(submissionRef(branch))
	if err != nil {
		return nil, false, err
	}
	for _, a := range annotations {
		if a.Ref != submissionRef(branch) {
			continue
		}
		lines := strings.Split(strings.TrimSpace(a.Message), "\n")
		resp := &Submission{PullRequestURL: lines[0]}
		if len(lines) > 1 {
			var n int64
			fmt.Sscanf(lines[1], "%d", &n)
			resp.PullRequestNumber = n
		}
		return resp, true, nil
	}
	return nil, false, nil
}

// PushRefs returns the annotation refs that should travel upstream with
// branch, so endorsements and submission markers survive the loss of this
// component's volume.
func (s *Service) PushRefs(branch string) ([]string, error) {
	annotations, err := s.repo.Annotations(endorsementRefPrefixFor(branch))
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(annotations)+1)
	for _, a := range annotations {
		refs = append(refs, a.Ref)
	}
	return refs, nil
}

// ==========================================
// Clustering
// ==========================================

// ListClusters groups a skill's open proposals by whether they edit
// overlapping regions of the same files, and returns them most-corroborated
// first.
//
// Everything here is recomputed from branch state on each call; no cluster
// is ever stored. The output is a measurement of where independent agents
// converged, and deliberately not an opinion about which proposal is right -
// that judgment belongs to whoever reviews the pull request.
func (s *Service) ListClusters(ctx context.Context, skillFilter string, includeSingletons bool) ([]*Cluster, error) {
	all, err := s.ListProposals(ctx, skillFilter, "")
	if err != nil {
		return nil, err
	}

	bySkill := make(map[string][]*Proposal)
	for _, p := range all {
		bySkill[p.SkillName] = append(bySkill[p.SkillName], p)
	}

	var out []*Cluster
	for _, group := range bySkill {
		ranges := make([]map[string][]gitrepo.LineRange, len(group))
		for i, p := range group {
			base := plumbing.NewHash(p.BaseSHA)
			head := plumbing.NewHash(p.HeadSHA)
			r, err := s.repo.ChangedRanges(base, head)
			if err != nil {
				return nil, fmt.Errorf("proposals: computing changed ranges for %q: %w", p.Branch, err)
			}
			ranges[i] = r
		}

		for _, members := range connectedComponents(ranges) {
			if len(members) < 2 && !includeSingletons {
				continue
			}
			cluster := &Cluster{}
			agents := make(map[string]struct{})
			pathCounts := make(map[string]int)

			for _, i := range members {
				cluster.Proposals = append(cluster.Proposals, group[i])
				agents[group[i].AgentID] = struct{}{}
				for _, e := range group[i].Endorsements {
					if !e.Stale {
						agents[e.AgentID] = struct{}{}
					}
				}
				for p := range ranges[i] {
					pathCounts[p]++
				}
			}

			for p, n := range pathCounts {
				if n > 1 {
					cluster.ContestedPaths = append(cluster.ContestedPaths, p)
				}
			}
			sort.Strings(cluster.ContestedPaths)
			cluster.DistinctAgents = len(agents)
			out = append(out, cluster)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DistinctAgents != out[j].DistinctAgents {
			return out[i].DistinctAgents > out[j].DistinctAgents
		}
		return out[i].Proposals[0].Branch < out[j].Proposals[0].Branch
	})
	return out, nil
}

// connectedComponents unions proposals whose changed ranges overlap and
// returns the resulting index groups.
func connectedComponents(ranges []map[string][]gitrepo.LineRange) [][]int {
	parent := make([]int, len(ranges))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}

	for i := range ranges {
		for j := i + 1; j < len(ranges); j++ {
			if overlaps(ranges[i], ranges[j]) {
				if ri, rj := find(i), find(j); ri != rj {
					parent[ri] = rj
				}
			}
		}
	}

	groups := make(map[int][]int)
	for i := range ranges {
		root := find(i)
		groups[root] = append(groups[root], i)
	}

	out := make([][]int, 0, len(groups))
	for _, g := range groups {
		sort.Ints(g)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// overlaps reports whether two proposals touch a common region of a common
// file.
func overlaps(a, b map[string][]gitrepo.LineRange) bool {
	for filePath, aRanges := range a {
		bRanges, ok := b[filePath]
		if !ok {
			continue
		}
		for _, x := range aRanges {
			for _, y := range bRanges {
				// A whole-file marker (an add or a delete, which has no
				// base side to measure) contests every edit to that file.
				if isWholeFile(x) || isWholeFile(y) {
					return true
				}
				if x.Start-clusterContextLines < y.End+clusterContextLines &&
					y.Start-clusterContextLines < x.End+clusterContextLines {
					return true
				}
			}
		}
	}
	return false
}

func isWholeFile(r gitrepo.LineRange) bool {
	return r.Start == 0 && r.End == 0
}
