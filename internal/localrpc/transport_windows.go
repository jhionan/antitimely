package localrpc

import (
	"fmt"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const pipePrefix = `\\.\pipe\`

func DefaultPath(_ string) (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	return pipePrefix + "antitimely-" + sid, nil
}

func Dial(path string) (net.Conn, error) {
	if err := validatePipePath(path); err != nil {
		return nil, err
	}
	return winio.DialPipe(path, nil)
}

func Listen(path, _ string) (net.Listener, error) {
	if err := validatePipePath(path); err != nil {
		return nil, err
	}
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	// go-winio rejects remote clients. Restrict local access to this account.
	return winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")",
	})
}

func currentUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("get Windows user SID: %w", err)
	}
	return user.User.Sid.String(), nil
}

func validatePipePath(path string) error {
	name, ok := strings.CutPrefix(path, pipePrefix)
	if !ok || name == "" || strings.ContainsAny(name, `\/`) {
		return fmt.Errorf("expected a local named pipe path (%sNAME)", pipePrefix)
	}
	return nil
}
