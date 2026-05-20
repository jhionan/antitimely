package domain

import (
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
	if r.MatchCwdPrefix != nil && !strings.HasPrefix(sig.Cwd, *r.MatchCwdPrefix) {
		return false
	}
	return true
}
