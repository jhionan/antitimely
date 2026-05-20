// Package rpcapi defines the shared types for daemon ↔ CLI net/rpc traffic.
// Both daemon and CLI import this package; neither imports the other.
package rpcapi

const ServiceName = "Antitimely"

// --- Status ---

type StatusArgs struct{}

type StatusReply struct {
	ActiveProjects            []string
	TodayTotalsSeconds        map[string]int64
	UnassignedTodaySeconds    int64
	UnassignedSignaturesCount int
	UserIdleSeconds           int
	TickIntervalSeconds       int
	PermissionState           string // "ok" | "accessibility_denied" | "unknown"
}

// --- Allowlist ---

type WatchAddArgs struct {
	Kind       string // "bundle" | "binary"
	Identifier string
}
type WatchAddReply struct{}

type WatchRemoveArgs struct {
	Kind       string
	Identifier string
}
type WatchRemoveReply struct{}

type WatchListArgs struct{}
type WatchListReply struct {
	Items []WatchedItem
}
type WatchedItem struct {
	Kind       string
	Identifier string
}

// --- Projects ---

type ProjectAddArgs struct{ Name string }
type ProjectAddReply struct{ ID int64 }

type ProjectListArgs struct{}
type ProjectListReply struct{ Items []Project }
type Project struct {
	ID   int64
	Name string
}

type ProjectDeleteArgs struct{ Name string }
type ProjectDeleteReply struct{}

// --- Review ---

type PendingReviewArgs struct{ Limit int }
type PendingReviewReply struct{ Signatures []Signature }
type Signature struct {
	ObservationID int64
	Source        string
	BundleID      string
	WindowTitle   string
	BinaryName    string
	CWD           string
	Ticks         int64
	LastSeenUnix  int64
}

type TagSignatureArgs struct {
	ObservationID int64
	ProjectName   string
	CreateProject bool
	Rule          *ProposedRule
}
type ProposedRule struct {
	Priority         int64
	MatchBundleID    string
	MatchTitleSubstr string
	MatchBinaryName  string
	MatchCWDPrefix   string
}
type TagSignatureReply struct {
	RuleCreated   bool
	RuleID        int64
	TicksRetagged int64
}

type IgnoreSignatureArgs struct{ ObservationID int64 }
type IgnoreSignatureReply struct{}

// --- Rules ---

type RulesListArgs struct{}
type RulesListReply struct{ Items []Rule }
type Rule struct {
	ID               int64
	ProjectName      string
	Priority         int64
	MatchBundleID    string
	MatchTitleSubstr string
	MatchBinaryName  string
	MatchCWDPrefix   string
}

type RuleDeleteArgs struct{ ID int64 }
type RuleDeleteReply struct{}

// --- Reporting ---

type ReportArgs struct {
	FromUnix int64
	ToUnix   int64
}
type ReportReply struct {
	Totals     map[string]int64
	Unassigned int64
}
