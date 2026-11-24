package rpcapi

// ClickUpTask identifies a task cached from the connected account.
type ClickUpTask struct {
	ConnectionID string
	WorkspaceID  string
	TaskID       string
	Name         string
}

type ClickUpChoice struct {
	ClickUpTask
	Workspace string
	ParentID  string
}

type ClickUpSignInArgs struct{}

type ClickUpSignInReply struct {
	AuthorizationURL string
}

type ClickUpCatalogArgs struct{ Refresh bool }

type ClickUpCatalogReply struct {
	Connected bool
	Username  string
	Tasks     []ClickUpChoice
	Stale     bool
	Error     string
}

type ClickUpTimer = Timer
type ClickUpEntry = TimeEntry
type ClickUpStatusArgs = TimerStatusArgs
type ClickUpStatusReply = TimerStatusReply

type ClickUpSwitchArgs struct {
	// A nil task stops the running timer.
	Task *ClickUpTask
}

type ClickUpSelection struct {
	ClickUpTask
	ProjectID    int64
	StartUnix    int64
	EndUnix      int64
	Seconds      int64
	Confirmation string
}

type ClickUpPreview struct {
	Seconds          int64
	AvailableSeconds int64
	Confirmation     string
}

type ClickUpTaskDetailsArgs struct{ TaskID string }

type ClickUpAttachment struct {
	Name, URL string
	Image     bool
}
type ClickUpComment struct{ Author, Text string }
type ClickUpTaskDetailsReply struct {
	Name, Description, Status, Priority, URL string
	DueUnixMillis                            int64
	Attachments                              []ClickUpAttachment
	Comments                                 []ClickUpComment
}
