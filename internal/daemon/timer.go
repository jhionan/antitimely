package daemon

import (
	"errors"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
	"github.com/rian/antitimely/internal/tasktimer"
)

func (s *AntitimelyService) TimerStart(args rpcapi.TimerStartArgs, reply *rpcapi.Timer) error {
	if s.Tasks == nil {
		return errors.New("task timer unavailable")
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	timer, err := s.Tasks.StartLocal(ctx, time.Now().Unix(), args.Name)
	if err == nil {
		*reply = timerReply(timer)
	}
	return err
}

func (s *AntitimelyService) TimerStop(_ rpcapi.TimerStopArgs, reply *rpcapi.Timer) error {
	return s.ClickUpSwitch(rpcapi.ClickUpSwitchArgs{}, reply)
}

func (s *AntitimelyService) TimerStatus(_ rpcapi.TimerStatusArgs, reply *rpcapi.TimerStatusReply) error {
	if s.Tasks == nil {
		return errors.New("task timer unavailable")
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	timer, entries, err := s.Tasks.Snapshot(ctx, 40)
	if err != nil {
		return err
	}
	_ = s.SyncStatus(rpcapi.SyncStatusArgs{}, &reply.Sync)
	reply.Timer = timerReply(timer)
	for _, e := range entries {
		reply.Entries = append(reply.Entries, rpcapi.TimeEntry{
			ClickUpTask: rpcapi.ClickUpTask(e.Task), Start: e.Start, End: e.End,
			State: e.State, Message: e.Message, ProviderEntryID: e.ProviderEntryID,
		})
	}
	return nil
}

func timerReply(timer tasktimer.Timer) rpcapi.Timer {
	return rpcapi.Timer{ClickUpTask: rpcapi.ClickUpTask(timer.Task), StartedUnix: timer.StartedUnix, Local: timer.Local}
}
