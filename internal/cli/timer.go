package cli

import (
	"fmt"
	"os"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdTimer(args []string) int {
	if len(args) == 1 {
		switch args[0] {
		case "start":
			return callJSONRPC("TimerStart", &rpcapi.TimerStartArgs{}, &rpcapi.Timer{}, false)
		case "stop":
			return callJSONRPC("TimerStop", rpcapi.TimerStopArgs{}, &rpcapi.Timer{}, false)
		case "status":
			return callJSONRPC("TimerStatus", rpcapi.TimerStatusArgs{}, &rpcapi.TimerStatusReply{}, false)
		}
	}
	if len(args) == 2 && args[0] == "start" {
		request := &rpcapi.TimerStartArgs{}
		readInput := args[1] == "--stdin"
		if !readInput {
			request.Name = args[1]
		}
		return callJSONRPC("TimerStart", request, &rpcapi.Timer{}, readInput)
	}
	fmt.Fprintln(os.Stderr, `usage: antitimely timer start ["session name"] | stop | status`)
	return 64
}
