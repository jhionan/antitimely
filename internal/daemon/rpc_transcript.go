package daemon

import (
	"github.com/rian/antitimely/internal/rpcapi"
	"github.com/rian/antitimely/internal/store"
)

// TranscriptImport replays historical Claude Code transcripts into ticks for
// the given time range. Existing (ts, project_id) pairs are skipped so the
// call is safe to re-run.
func (s *AntitimelyService) TranscriptImport(args rpcapi.TranscriptImportArgs, reply *rpcapi.TranscriptImportReply) error {
	n, err := importTranscripts(s.DB, store.New(s.DB), s.Cache.Snapshot(), s.TranscriptRoot, args.FromUnix, args.ToUnix, s.TranscriptGraceSec, s.TickIntervalSeconds)
	if err != nil {
		return err
	}
	reply.Inserted = n
	return nil
}
