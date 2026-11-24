package daemon

import (
	"errors"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
	"github.com/rian/antitimely/internal/tasktimer"
)

func (s *AntitimelyService) ClickUpSignIn(_ rpcapi.ClickUpSignInArgs, reply *rpcapi.ClickUpSignInReply) error {
	if s.ClickUp == nil {
		return errors.New("ClickUp sign-in requires a configured sync device")
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	url, err := s.ClickUp.BeginClickUpSignIn(ctx)
	reply.AuthorizationURL = url
	return err
}

func (s *AntitimelyService) ClickUpCatalog(args rpcapi.ClickUpCatalogArgs, reply *rpcapi.ClickUpCatalogReply) error {
	if s.Tasks == nil {
		return errors.New("task timer unavailable")
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	var catalog tasktimer.Catalog
	var err error
	if args.Refresh && s.ClickUp != nil {
		catalog, err = s.ClickUp.Refresh(ctx)
	} else {
		catalog, err = s.Tasks.Catalog(ctx)
		catalog.Stale = s.ClickUp == nil
		if catalog.Stale {
			catalog.Error = "ClickUp requires a configured sync device"
		}
	}
	if err != nil {
		return err
	}
	reply.Connected, reply.Username, reply.Stale, reply.Error = catalog.Connected, catalog.Username, catalog.Stale, catalog.Error
	for _, c := range catalog.Tasks {
		reply.Tasks = append(reply.Tasks, rpcapi.ClickUpChoice{ClickUpTask: rpcapi.ClickUpTask(c.Task), Workspace: c.Workspace, ParentID: c.ParentID})
	}
	return nil
}

func (s *AntitimelyService) ClickUpStatus(args rpcapi.ClickUpStatusArgs, reply *rpcapi.ClickUpStatusReply) error {
	return s.TimerStatus(args, reply)
}

func (s *AntitimelyService) ClickUpSwitch(args rpcapi.ClickUpSwitchArgs, reply *rpcapi.ClickUpTimer) error {
	if s.Tasks == nil {
		return errors.New("task timer unavailable")
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	timer, err := s.Tasks.Switch(ctx, time.Now().Unix(), (*tasktimer.Task)(args.Task))
	if err == nil {
		*reply = timerReply(timer)
	}
	return err
}

func (s *AntitimelyService) ClickUpPreview(args rpcapi.ClickUpSelection, reply *rpcapi.ClickUpPreview) error {
	if s.Tasks == nil {
		return errors.New("task timer unavailable")
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	preview, err := s.Tasks.Preview(ctx, time.Now().Unix(), clickUpSelection(args), tasktimer.Task(args.ClickUpTask))
	*reply = rpcapi.ClickUpPreview(preview)
	return err
}

func (s *AntitimelyService) ClickUpAllocate(args rpcapi.ClickUpSelection, reply *rpcapi.ClickUpPreview) error {
	if s.Tasks == nil {
		return errors.New("task timer unavailable")
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	preview, err := s.Tasks.Allocate(ctx, time.Now().Unix(), clickUpSelection(args), tasktimer.Task(args.ClickUpTask), args.Confirmation)
	*reply = rpcapi.ClickUpPreview(preview)
	return err
}

func clickUpSelection(args rpcapi.ClickUpSelection) tasktimer.Selection {
	return tasktimer.Selection{ProjectID: args.ProjectID, StartUnix: args.StartUnix, EndUnix: args.EndUnix, Seconds: args.Seconds}
}

func (s *AntitimelyService) ClickUpTaskDetails(args rpcapi.ClickUpTaskDetailsArgs, reply *rpcapi.ClickUpTaskDetailsReply) error {
	if s.ClickUp == nil {
		return errors.New("ClickUp details require a configured sync device")
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	task, err := s.ClickUp.TaskDetails(ctx, args.TaskID)
	if err != nil {
		return err
	}
	*reply = rpcapi.ClickUpTaskDetailsReply{Name: task.Name, Description: task.Description, Status: task.Status, Priority: task.Priority, DueUnixMillis: task.DueUnixMillis, URL: task.Url}
	for _, a := range task.Attachments {
		reply.Attachments = append(reply.Attachments, rpcapi.ClickUpAttachment{Name: a.Name, URL: a.Url, Image: a.Image})
	}
	for _, c := range task.Comments {
		reply.Comments = append(reply.Comments, rpcapi.ClickUpComment{Author: c.Author, Text: c.Text})
	}
	return nil
}
