// Package rpcapi defines the shared types for daemon ↔ CLI net/rpc traffic.
// Both daemon and CLI import this package; neither imports the other.
package rpcapi

const ServiceName = "Antitimely"

// --- Status ---

type StatusArgs struct{}

type StatusReply struct {
	// Legacy fields kept for backward compatibility:
	ActiveProjects            []string
	TodayTotalsSeconds        map[string]int64
	UnassignedTodaySeconds    int64
	UnassignedSignaturesCount int
	UserIdleSeconds           int
	TickIntervalSeconds       int
	PermissionState           string // "ok" | "accessibility_denied" | "unknown"

	// New grouped fields:
	TodayTotalSeconds         int64          // sum of all project ticks today (distinct ts)
	Companies                 []CompanyTotals // ordered by company name; unassigned last
	UnassignedBillableSeconds int64          // all-time unassigned ticks in seconds
	DaemonUptimeSeconds       int64          // seconds since the daemon started
}

type CompanyTotals struct {
	Name            string
	LastInvoiceUnix int64 // 0 if no invoices
	BillableSeconds int64 // sum of projects' billable seconds (since last invoice, or all-time if none)
	TodaySeconds    int64 // sum of projects' today seconds
	Projects        []ProjectTotals
}

type ProjectTotals struct {
	Name            string
	BillableSeconds int64 // since company's last invoice (or all-time if no invoice)
	TodaySeconds    int64
	Paused          bool
	Armed           bool
	// SuppressedSeconds is agent time dropped while the project is armed
	// (pending focus / auto-disarm). Surfaced so an armed project that is
	// silently not counting becomes visible instead of just losing the time.
	SuppressedSeconds int64
}

// --- LatestTick: cheap probe for the live status view ---

type LatestTickArgs struct{}

// LatestTickReply carries only the cheap-to-compute live fields. The live
// status view polls this every refresh (~instant: one indexed MAX(ts) plus
// cached idle), and recomputes the expensive grouped totals via Status only
// when LatestTickUnix advances (a new tick was recorded) or the day rolls over.
type LatestTickReply struct {
	LatestTickUnix      int64  // newest tick ts, 0 if no ticks yet
	UserIdleSeconds     int    // same source as StatusReply.UserIdleSeconds
	DaemonUptimeSeconds int64  // same source as StatusReply.DaemonUptimeSeconds
	PermissionState     string // same source as StatusReply.PermissionState
}

// --- Invoices ---

type InvoiceSendArgs struct {
	CompanyName string
	SentAtUnix  int64  // 0 = use server's "now"
	Note        string
}
type InvoiceSendReply struct{ ID int64 }

type InvoiceListArgs struct {
	CompanyName string // empty = list all
}
type InvoiceListReply struct{ Items []InvoiceEntry }
type InvoiceEntry struct {
	ID          int64
	CompanyName string
	SentAtUnix  int64
	Note        string
}

type InvoiceDeleteArgs struct{ ID int64 }
type InvoiceDeleteReply struct{}

type InvoiceGenerateArgs struct {
	CompanyName   string
	FromUnix      int64 // 0 = use default period for the company's billing_mode
	ToUnix        int64 // 0 = use default
	IssueDateUnix int64 // 0 = now
	Note          string
	DryRun        bool
	AllowEmpty    bool
	DiscountCents int64 // flat discount in the company's currency; 0 = none
}

type InvoiceGenerateReply struct {
	InvoiceID     int64 // 0 when DryRun=true
	Number        string
	PDFPath       string
	TotalCents    int64
	Currency      string
	SenderKey     string
	IssueDateUnix int64
	DueDateUnix   int64
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

type ProjectAddArgs struct {
	Name        string
	CompanyName string // optional; empty = no company at creation
}
type ProjectAddReply struct{ ID int64 }

type ProjectListArgs struct{}
type ProjectListReply struct{ Items []Project }
type Project struct {
	ID          int64
	Name        string
	CompanyName string // empty if no company
	Paused      bool
}

type ProjectDeleteArgs struct{ Name string }
type ProjectDeleteReply struct{}

type ProjectPauseArgs struct{ Name string }
type ProjectPauseReply struct{}

type ProjectResumeArgs struct{ Name string }
type ProjectResumeReply struct{}

type ProjectPauseAllArgs struct{}
type ProjectPauseAllReply struct{ Count int64 }

type ProjectResumeAllArgs struct{}
type ProjectResumeAllReply struct{ Count int64 }

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

// --- Summary ---

type SummaryArgs struct {
	FromUnix    int64
	ToUnix      int64
	ProjectName string // optional filter
	CompanyName string // optional filter
}
type SummaryReply struct {
	Companies         []SummaryCompany
	UnassignedSeconds int64
}
type SummaryCompany struct {
	Name     string
	Projects []SummaryProject
}
type SummaryProject struct {
	Name        string
	Seconds     int64
	CwdPrefixes []string // from cwd-prefix rules; may be empty if project has no cwd rules
}

// --- Companies ---

type CompanyAddArgs struct{ Name string }
type CompanyAddReply struct{ ID int64 }

type CompanyListArgs struct{}
type CompanyListReply struct{ Items []Company }
type Company struct {
	ID   int64
	Name string
}

type CompanyDeleteArgs struct{ Name string }
type CompanyDeleteReply struct{}

type ProjectSetCompanyArgs struct {
	ProjectName string
	CompanyName string // empty = unassign
}
type ProjectSetCompanyReply struct{}

type SetCompanyBillingArgs struct {
	Name        string
	BillingMode string
	Currency    string
	RateCents   int64
	BilledFrom  string
}
type SetCompanyBillingReply struct{}

// --- Transcript import ---

type TranscriptImportArgs struct{ FromUnix, ToUnix int64 }
type TranscriptImportReply struct{ Inserted int }
