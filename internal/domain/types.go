package domain

// Source enumerates where a Signal came from.
type Source string

const (
	SourceFocus Source = "focus"
	SourceAgent Source = "agent"
)

// Signal is one observation produced by the polling loop in a single tick.
// Fields use empty string (not pointer) for "not applicable" — matches the
// observations table's NOT NULL DEFAULT '' convention.
type Signal struct {
	Source     Source
	BundleID   string
	Title      string
	BinaryName string
	CWD        string
}

func (s Signal) IsAgent() bool { return s.Source == SourceAgent }
func (s Signal) IsFocus() bool { return s.Source == SourceFocus }

// RuleSpec is a rule as it lives in memory inside the daemon's cache.
// Match columns use *string: nil = "don't care", non-nil = check this clause.
type RuleSpec struct {
	ID               int64
	ProjectID        int64
	Priority         int64
	MatchBundleID    *string
	MatchTitleSubstr *string
	MatchBinaryName  *string
	MatchCWDPrefix   *string
}

// ProposedRule is what generalize.go produces — a draft rule for confirmation
// in the review flow before being persisted via AddRule.
type ProposedRule struct {
	Priority         int64
	MatchBundleID    *string
	MatchTitleSubstr *string
	MatchBinaryName  *string
	MatchCWDPrefix   *string
}
