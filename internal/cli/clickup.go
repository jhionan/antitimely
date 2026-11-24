package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdClickUp(args []string) int {
	if len(args) != 1 {
		return clickUpUsage()
	}
	var method string
	var request, response any
	var readInput bool
	switch args[0] {
	case "details":
		method, request, response = "ClickUpTaskDetails", &rpcapi.ClickUpTaskDetailsArgs{}, &rpcapi.ClickUpTaskDetailsReply{}
		readInput = true
	case "sign-in":
		method, request, response = "ClickUpSignIn", rpcapi.ClickUpSignInArgs{}, &rpcapi.ClickUpSignInReply{}
	case "catalog", "refresh":
		method = "ClickUpCatalog"
		request = rpcapi.ClickUpCatalogArgs{Refresh: args[0] == "refresh"}
		response = &rpcapi.ClickUpCatalogReply{}
	case "status":
		method, request, response = "ClickUpStatus", rpcapi.ClickUpStatusArgs{}, &rpcapi.ClickUpStatusReply{}
	case "start":
		method, request, response = "ClickUpSwitch", &rpcapi.ClickUpSwitchArgs{Task: &rpcapi.ClickUpTask{}}, &rpcapi.ClickUpTimer{}
		readInput = true
	case "stop":
		method, request, response = "ClickUpSwitch", rpcapi.ClickUpSwitchArgs{}, &rpcapi.ClickUpTimer{}
	case "preview", "allocate":
		method = "ClickUpPreview"
		if args[0] == "allocate" {
			method = "ClickUpAllocate"
		}
		request, response = &rpcapi.ClickUpSelection{}, &rpcapi.ClickUpPreview{}
		readInput = true
	default:
		return clickUpUsage()
	}
	return callJSONRPC(method, request, response, readInput)
}

func callJSONRPC(method string, request, response any, readInput bool) int {
	if readInput {
		decoder := json.NewDecoder(io.LimitReader(os.Stdin, 16*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(request); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 64
		}
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	if err := client.Call(rpcapi.ServiceName+"."+method, request, response); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(response); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func clickUpUsage() int {
	fmt.Fprintln(os.Stderr, "usage: antitimely clickup sign-in|details|catalog|refresh|status|start|stop|preview|allocate")
	return 64
}
