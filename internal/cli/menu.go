package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// IsStdinTerminal reports whether stdin appears to be an interactive terminal.
// Returns false if stdin is a pipe, file, or other non-tty.
func IsStdinTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// RunMenu launches the top-level interactive menu and loops until the user quits.
func RunMenu() int {
	stdin := bufio.NewScanner(os.Stdin)
	for {
		fmt.Println()
		fmt.Println("antitimely — work time tracker")
		fmt.Println()
		fmt.Println("  [1] Status")
		fmt.Println("  [2] Review unassigned signatures")
		fmt.Println("  [3] Report (today)")
		fmt.Println("  [4] Watch programs")
		fmt.Println("  [5] Projects")
		fmt.Println("  [6] Companies")
		fmt.Println("  [7] Rules")
		fmt.Println("  [h] Help (full CLI usage)")
		fmt.Println("  [q] Quit")
		fmt.Print("\nChoice: ")
		if !stdin.Scan() {
			return 0
		}
		choice := strings.TrimSpace(stdin.Text())
		switch choice {
		case "1":
			cmdStatus(nil)
		case "2":
			cmdReview(nil)
		case "3":
			cmdReport(nil)
		case "4":
			watchMenu(stdin)
		case "5":
			projectMenu(stdin)
		case "6":
			companyMenu(stdin)
		case "7":
			rulesMenu(stdin)
		case "h", "help":
			printUsage(os.Stdout)
		case "q", "Q", "":
			return 0
		default:
			fmt.Println("  invalid choice")
		}
	}
}

// promptLine prints a prompt and reads one trimmed line. Returns (text, ok).
// ok is false on EOF.
func promptLine(stdin *bufio.Scanner, label string) (string, bool) {
	fmt.Print(label)
	if !stdin.Scan() {
		return "", false
	}
	return strings.TrimSpace(stdin.Text()), true
}

func watchMenu(stdin *bufio.Scanner) {
	for {
		fmt.Println()
		fmt.Println("Watch programs:")
		fmt.Println("  [1] List watched programs")
		fmt.Println("  [2] Add app (by bundle id)")
		fmt.Println("  [3] Add binary (by process name)")
		fmt.Println("  [4] Remove")
		fmt.Println("  [b] Back")
		choice, ok := promptLine(stdin, "Choice: ")
		if !ok {
			return
		}
		switch choice {
		case "1":
			watchList()
		case "2":
			id, ok := promptLine(stdin, "Bundle id (e.g. com.google.antigravity-ide): ")
			if !ok || id == "" {
				continue
			}
			watchAdd([]string{"app", id})
		case "3":
			name, ok := promptLine(stdin, "Binary name (e.g. claude): ")
			if !ok || name == "" {
				continue
			}
			watchAdd([]string{"binary", name})
		case "4":
			id, ok := promptLine(stdin, "Identifier to remove: ")
			if !ok || id == "" {
				continue
			}
			watchRemove([]string{id})
		case "b", "":
			return
		default:
			fmt.Println("  invalid choice")
		}
	}
}

func projectMenu(stdin *bufio.Scanner) {
	for {
		fmt.Println()
		fmt.Println("Projects:")
		fmt.Println("  [1] List projects")
		fmt.Println("  [2] Add project")
		fmt.Println("  [3] Delete project")
		fmt.Println("  [4] Assign company to project")
		fmt.Println("  [b] Back")
		choice, ok := promptLine(stdin, "Choice: ")
		if !ok {
			return
		}
		switch choice {
		case "1":
			projectList()
		case "2":
			name, ok := promptLine(stdin, "Project name: ")
			if !ok || name == "" {
				continue
			}
			company, _ := promptLine(stdin, "Company (blank for none): ")
			if company == "" {
				projectAdd([]string{name})
			} else {
				projectAdd([]string{"--company=" + company, name})
			}
		case "3":
			name, ok := promptLine(stdin, "Project name to delete: ")
			if !ok || name == "" {
				continue
			}
			projectDelete([]string{name})
		case "4":
			name, ok := promptLine(stdin, "Project name: ")
			if !ok || name == "" {
				continue
			}
			company, _ := promptLine(stdin, "Company (blank to unassign): ")
			if company == "" {
				projectSetCompany([]string{name})
			} else {
				projectSetCompany([]string{name, company})
			}
		case "b", "":
			return
		default:
			fmt.Println("  invalid choice")
		}
	}
}

func companyMenu(stdin *bufio.Scanner) {
	for {
		fmt.Println()
		fmt.Println("Companies:")
		fmt.Println("  [1] List companies")
		fmt.Println("  [2] Add company")
		fmt.Println("  [3] Delete company")
		fmt.Println("  [b] Back")
		choice, ok := promptLine(stdin, "Choice: ")
		if !ok {
			return
		}
		switch choice {
		case "1":
			companyList()
		case "2":
			name, ok := promptLine(stdin, "Company name: ")
			if !ok || name == "" {
				continue
			}
			companyAdd([]string{name})
		case "3":
			name, ok := promptLine(stdin, "Company name to delete: ")
			if !ok || name == "" {
				continue
			}
			companyDelete([]string{name})
		case "b", "":
			return
		default:
			fmt.Println("  invalid choice")
		}
	}
}

func rulesMenu(stdin *bufio.Scanner) {
	for {
		fmt.Println()
		fmt.Println("Rules:")
		fmt.Println("  [1] List rules")
		fmt.Println("  [2] Delete rule")
		fmt.Println("  [b] Back")
		choice, ok := promptLine(stdin, "Choice: ")
		if !ok {
			return
		}
		switch choice {
		case "1":
			rulesList()
		case "2":
			idStr, ok := promptLine(stdin, "Rule ID to delete: ")
			if !ok || idStr == "" {
				continue
			}
			if _, err := strconv.ParseInt(idStr, 10, 64); err != nil {
				fmt.Fprintln(os.Stderr, "invalid id")
				continue
			}
			rulesDelete([]string{idStr})
		case "b", "":
			return
		default:
			fmt.Println("  invalid choice")
		}
	}
}
