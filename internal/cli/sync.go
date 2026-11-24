package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdSync(args []string) int {
	if len(args) != 1 || args[0] != "status" {
		fmt.Fprintln(os.Stderr, "usage: antitimely sync status")
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var status rpcapi.SyncStatusReply
	if err := client.Call(rpcapi.ServiceName+".SyncStatus", rpcapi.SyncStatusArgs{}, &status); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !status.Configured {
		fmt.Println("Sync is disabled.")
	} else {
		fmt.Printf("Queued changes: %d\n", status.Pending)
		if status.LastSuccessUnix != 0 {
			fmt.Println("Last successful upload:", time.Unix(status.LastSuccessUnix, 0).Format(time.RFC3339))
		}
	}
	if status.Error != "" {
		fmt.Fprintln(os.Stderr, status.Error)
		return 1
	}
	return 0
}
