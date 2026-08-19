package domain

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// MatchRules returns the project_id of the first rule that matches sig, or nil
// if none match. Rules are evaluated in priority order, ascending (lowest
// priority number first); within a priority, rule id ascending.
//
// A rule matches when every non-nil match_* field on the rule equals (for
// bundle/binary), is a substring of (for title), or is a path-prefix of (for
// cwd) the corresponding field on sig. Nil match_* = "don't care."
//
// This is the runtime equivalent of the ApplyRuleRetroactively SQL query.
func MatchRules(sig Signal, rules []RuleSpec) *int64 {
	sorted := make([]RuleSpec, len(rules))
	copy(sorted, rules)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}
		return sorted[i].ID < sorted[j].ID
	})

	for _, r := range sorted {
		if matchOne(sig, r) {
			pid := r.ProjectID
			return &pid
		}
	}
	return nil
}

func matchOne(sig Signal, r RuleSpec) bool {
	if r.MatchBundleID != nil && sig.BundleID != *r.MatchBundleID {
		return false
	}
	if r.MatchTitleSubstr != nil && !strings.Contains(sig.WindowTitle, *r.MatchTitleSubstr) {
		return false
	}
	if r.MatchBinaryName != nil && sig.BinaryName != *r.MatchBinaryName {
		return false
	}
	if r.MatchCwdPrefix != nil && !MatchesCwd(*r.MatchCwdPrefix, sig.Cwd) {
		return false
	}
	if r.MatchSpaceID != nil && sig.SpaceID != *r.MatchSpaceID {
		return false
	}
	return true
}

// MatchesCwd reports whether cwd satisfies a rule's cwd clause.
//
// A pattern containing '*' is a glob: it is tested with path.Match against cwd
// and each of its ancestor directories, so a pattern naming a worktree also
// matches build directories nested inside it. A pattern without '*' keeps the
// original literal-prefix rule: equal, or a true subdirectory.
func MatchesCwd(pattern, cwd string) bool {
	p := strings.TrimRight(pattern, "/")
	if p == "" {
		return false
	}
	if !strings.Contains(p, "*") {
		return cwd == p || strings.HasPrefix(cwd, p+"/")
	}
	for cur := cwd; ; {
		if ok, err := path.Match(p, cur); err == nil && ok {
			return true
		}
		parent := path.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}

// ValidateCwdPattern rejects patterns whose '*' is not confined to the final
// path segment. SQLite's GLOB lets '*' cross '/' while path.Match does not, so
// a star earlier in the path would make the live matcher and the retroactive
// SQL disagree.
func ValidateCwdPattern(pattern string) error {
	p := strings.TrimRight(pattern, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 && strings.Contains(p[:i], "*") {
		return fmt.Errorf("'*' is only allowed in the final path segment: %q", pattern)
	}
	return nil
}
