package rpcapi

type TimerStartArgs struct {
	Name string
}

type TimerStopArgs struct{}
type TimerStatusArgs struct{}

type Timer struct {
	ClickUpTask // For a local timer, only Name is set.
	StartedUnix int64
	Local       bool
}

type TimeEntry struct {
	ClickUpTask
	Start, End      int64
	State           string
	Message         string
	ProviderEntryID string
}

type TimerStatusReply struct {
	Sync    SyncStatusReply
	Timer   Timer
	Entries []TimeEntry
}
