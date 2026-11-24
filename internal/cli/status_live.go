//go:build darwin || windows

package cli

import (
	"errors"
	"fmt"
	"net/rpc"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func runStatusLive(client *rpc.Client) int {
	st, err := enterStatusTerminal()
	if err != nil {
		return renderOnce(client)
	}

	out := os.Stdout
	var restoreOnce sync.Once
	done := make(chan struct{})
	cleanup := func() {
		restoreOnce.Do(func() {
			altScreenLeave(out)
			showCursor(out)
			st.restore()
			close(done)
		})
	}
	defer cleanup()

	// Always restore the terminal on any terminal/job-control signal. Cbreak
	// keeps ISIG, so Ctrl-C (SIGINT), Ctrl-\ (SIGQUIT) and Ctrl-Z (SIGTSTP)
	// still fire; catching them here prevents leaving the terminal in raw +
	// alt-screen mode. The goroutine also exits on normal teardown (done) so
	// it does not leak across repeated menu visits.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, statusSignals...)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case sig := <-sigCh:
			cleanup()
			code := 130
			if s, ok := sig.(syscall.Signal); ok {
				code = 128 + int(s)
			}
			os.Exit(code)
		case <-done:
		}
	}()

	altScreenEnter(out)
	hideCursor(out)
	quietTerminalInput(out) // stop focus/paste escape sequences from closing the view

	// reply holds the last expensive Status snapshot; it is recomputed only when
	// the cheap LatestTick probe shows a new tick (or the day rolls / probe
	// fails). The header's cheap live fields (idle, uptime, permission) are
	// refreshed from the probe every cycle so the view still feels live.
	var reply rpcapi.StatusReply
	haveData := false
	var lastErr error
	lastTs := int64(-1)
	lastDay := 0
	for {
		// (Re)connect if we have no live client. A daemon restart makes the
		// previous connection permanently return ErrShutdown, so on any RPC
		// error below we drop the client and reconnect here next cycle. This is
		// what lets the view survive a daemon restart instead of getting stuck
		// forever on "connection is shut down".
		if client == nil {
			c, derr := redial()
			if derr != nil {
				lastErr = derr
			} else {
				client = c
				lastTs = -1
			}
		}

		if client != nil {
			probe, perr := liveStatusRPC[rpcapi.LatestTickReply](client, "LatestTick", rpcapi.LatestTickArgs{})
			if perr != nil {
				client.Close()
				client = nil
				lastErr = perr
			} else {
				lastErr = nil
				curDay := localDayKey(time.Now())
				if !haveData || statusBodyChanged(lastTs, probe.LatestTickUnix, lastDay, curDay, false) {
					full, ferr := liveStatusRPC[rpcapi.StatusReply](client, "Status", rpcapi.StatusArgs{})
					if ferr != nil {
						client.Close()
						client = nil
						lastErr = ferr
					} else {
						reply = full
						haveData = true
						lastDay = curDay
						lastTs = probe.LatestTickUnix
					}
				}
				// Refresh the cheap live header fields from the probe (only if
				// the connection is still good after the optional heavy fetch).
				if client != nil {
					reply.UserIdleSeconds = probe.UserIdleSeconds
					reply.DaemonUptimeSeconds = probe.DaemonUptimeSeconds
					reply.PermissionState = probe.PermissionState
				}
			}
		}

		clearScreen(out)
		if haveData {
			renderStatus(out, reply)
			renderWarning(out, reply)
		} else {
			fmt.Fprintln(out, "Connecting to daemon…")
		}
		if lastErr != nil {
			fmt.Fprintf(out, "\n⚠ daemon not responding, reconnecting: %v\n", lastErr)
		}
		renderFooter(out, time.Now())

		if st.readEvent() == evtEsc {
			if client != nil {
				client.Close()
			}
			return 0 // cleanup runs via defer
		}
	}
}

// Limit view latency if the daemon stops responding mid-call.
func liveStatusRPC[T any](client *rpc.Client, method string, args any) (T, error) {
	var reply T
	call := client.Go(rpcapi.ServiceName+"."+method, args, &reply, nil)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case completed := <-call.Done:
		return reply, completed.Error
	case <-timer.C:
		_ = client.Close()
		var empty T
		return empty, errors.New("status RPC timed out")
	}
}
