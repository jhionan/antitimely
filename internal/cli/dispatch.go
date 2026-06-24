package cli

import (
	"fmt"
	"os"
)

// Dispatch parses os.Args[1:] and routes to the right subcommand.
func Dispatch(args []string) int {
	if len(args) == 0 {
		if IsStdinTerminal() {
			return RunMenu()
		}
		printUsage(os.Stderr)
		return 64
	}
	switch args[0] {
	case "help", "--help", "-h":
		printUsage(os.Stdout)
		return 0
	case "status":
		return cmdStatus(args[1:])
	case "watch":
		return cmdWatch(args[1:])
	case "company":
		return cmdCompany(args[1:])
	case "invoice":
		return cmdInvoice(args[1:])
	case "project":
		return cmdProject(args[1:])
	case "end-day":
		return projectPauseAll()
	case "start-day":
		return projectResumeAll()
	case "review":
		return cmdReview(args[1:])
	case "rules":
		return cmdRules(args[1:])
	case "report":
		return cmdReport(args[1:])
	case "summary":
		return cmdSummary(args[1:])
	case "reset":
		return cmdReset(args[1:])
	case "config":
		return cmdConfig(args[1:])
	case "debug":
		return cmdDebug(args[1:])
	case "install-launch-agent":
		return cmdInstallLaunchAgent(args[1:])
	case "uninstall-launch-agent":
		return cmdUninstallLaunchAgent(args[1:])
	case "restart":
		return cmdRestartDaemon(args[1:])
	case "import-transcripts":
		return importTranscriptsCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		printUsage(os.Stderr)
		return 64
	}
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, `antitimely — work time tracker by project

Usage:
  antitimely daemon  [--interval=5s] [--idle-thresh=120s] [--agent-cpu-thresh=5]
  antitimely status
  antitimely watch  add app|binary <id>  |  list  |  remove <id>
  antitimely company  add <name>  |  list  |  delete <name>
  antitimely invoice  generate <company> [--from=YYYY-MM-DD] [--to=YYYY-MM-DD] [--issue-date=YYYY-MM-DD] [--note=...] [--dry-run] [--allow-empty]
                      send [--at=YYYY-MM-DD] [--note=...] <company>   (anchor only, no PDF — kept for back-compat)
                      list [<company>]  |  delete <id>  |  show-senders
  antitimely project  add [--company=<name>] <name>  |  list  |  delete <name>  |  set-company <project> [<company>]  |  pause <name>  |  resume <name>  |  pause-all  |  resume-all
  antitimely end-day                  shortcut for 'project pause-all' (use to stop all tracking at end of day)
  antitimely start-day                shortcut for 'project resume-all' (use to resume tracking at start of day)
  antitimely review
  antitimely rules    list  |  delete <id>
  antitimely report   [--from=YYYY-MM-DD] [--to=YYYY-MM-DD]
  antitimely summary  [--from=YYYY-MM-DD] [--to=YYYY-MM-DD] [--project=<name>] [--company=<name>] [--all-authors] [--md|--txt]
  antitimely reset    all  |  ticks   [--yes]
  antitimely config   init  |  show  |  path
  antitimely install-launch-agent     install ~/Library/LaunchAgents/com.rian.antitimely.plist and start daemon at login
  antitimely uninstall-launch-agent   remove launch agent and stop background daemon
  antitimely restart                  restart the background daemon (launchctl kickstart -k)
  antitimely debug   windows  |  frontmost  |  idle
  antitimely help    show this message

Run any subcommand with the daemon running ('antitimely daemon' in another terminal).
State lives in ~/.antitimely/ (db.sqlite, antitimely.sock, antitimely.pid).`)
}
