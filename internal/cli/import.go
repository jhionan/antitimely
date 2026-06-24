package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/rian/antitimely/internal/rpcapi"
)

func importTranscriptsCmd(args []string) int {
	fs := flag.NewFlagSet("import-transcripts", flag.ExitOnError)
	from := fs.String("from", "", "Start date: YYYY-MM-DD (default: beginning of time)")
	to := fs.String("to", "", "End date: YYYY-MM-DD (default: now)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	fromUnix, err := parseOptionalDate(*from)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --from:", err)
		return 64
	}
	toUnix, err := parseOptionalDate(*to)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --to:", err)
		return 64
	}

	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()

	var reply rpcapi.TranscriptImportReply
	if err := client.Call(rpcapi.ServiceName+".TranscriptImport",
		rpcapi.TranscriptImportArgs{FromUnix: fromUnix, ToUnix: toUnix}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Imported %d ticks\n", reply.Inserted)
	return 0
}
