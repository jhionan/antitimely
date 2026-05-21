package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdSummary(args []string) int {
	fs := flag.NewFlagSet("summary", flag.ExitOnError)
	fromStr := fs.String("from", "", "Inclusive start date YYYY-MM-DD (default today)")
	toStr := fs.String("to", "", "Exclusive end date YYYY-MM-DD (default tomorrow)")
	projectFilter := fs.String("project", "", "Filter to a specific project")
	companyFilter := fs.String("company", "", "Filter to a specific company")
	allAuthors := fs.Bool("all-authors", false, "Include commits by all authors (default: only yours)")
	asTxt := fs.Bool("txt", false, "Plain text output (default: markdown)")
	asMd := fs.Bool("md", false, "Markdown output (default; this flag is a no-op for explicitness)")
	_ = fs.Parse(args)
	_ = asMd

	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.Add(24 * time.Hour)
	if *fromStr != "" {
		t, err := time.ParseInLocation("2006-01-02", *fromStr, now.Location())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 64
		}
		start = t
	}
	if *toStr != "" {
		t, err := time.ParseInLocation("2006-01-02", *toStr, now.Location())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 64
		}
		end = t
	}

	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()

	var reply rpcapi.SummaryReply
	err := client.Call(rpcapi.ServiceName+".Summary", rpcapi.SummaryArgs{
		FromUnix:    start.Unix(),
		ToUnix:      end.Unix(),
		ProjectName: *projectFilter,
		CompanyName: *companyFilter,
	}, &reply)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Determine git author filter.
	var authorFilter string
	if !*allAuthors {
		out, err := exec.Command("git", "config", "--get", "user.email").Output()
		if err == nil {
			authorFilter = strings.TrimSpace(string(out))
		}
	}

	render(os.Stdout, reply, start, end, authorFilter, !*asTxt)
	return 0
}

// gitLog returns commit lines for the given directory in the date range, one
// per line, formatted "YYYY-MM-DD <short-sha> <subject>". Empty list if dir
// isn't a git repo or has no matching commits.
func gitLog(dir string, since, until time.Time, authorEmail string) []string {
	args := []string{
		"-C", dir,
		"log",
		"--no-merges",
		"--date=short",
		"--pretty=format:%ad %h %s",
		fmt.Sprintf("--since=%s", since.Format(time.RFC3339)),
		fmt.Sprintf("--until=%s", until.Format(time.RFC3339)),
	}
	if authorEmail != "" {
		args = append(args, "--author="+authorEmail)
	}
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		// not a git repo, or other transient — silent skip
		return nil
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func render(w io.Writer, r rpcapi.SummaryReply, start, end time.Time, authorEmail string, md bool) {
	days := int(end.Sub(start).Hours() / 24)
	if days < 1 {
		days = 1
	}

	if md {
		fmt.Fprintf(w, "# Work summary: %s → %s (%d day%s)\n\n",
			start.Format("2006-01-02"), end.Format("2006-01-02"), days, plural(days))
	} else {
		fmt.Fprintf(w, "Work summary: %s -> %s (%d day%s)\n\n",
			start.Format("2006-01-02"), end.Format("2006-01-02"), days, plural(days))
	}

	for _, comp := range r.Companies {
		// Skip "(unassigned)" bucket here — handled below via UnassignedSeconds.
		if comp.Name == "(unassigned)" {
			continue
		}

		// Only show companies that have at least one project with hours or commits.
		var sections []string
		for _, p := range comp.Projects {
			section := renderProject(p, start, end, authorEmail, md)
			if section != "" {
				sections = append(sections, section)
			}
		}
		if len(sections) == 0 {
			continue
		}

		if md {
			fmt.Fprintf(w, "## %s\n\n", comp.Name)
		} else {
			fmt.Fprintf(w, "%s\n%s\n\n", comp.Name, strings.Repeat("=", len(comp.Name)))
		}
		for _, s := range sections {
			fmt.Fprint(w, s)
		}
	}

	if r.UnassignedSeconds > 0 {
		if md {
			fmt.Fprintf(w, "## (unassigned)\n\n%s\n\n", fmtDuration(r.UnassignedSeconds))
		} else {
			fmt.Fprintf(w, "(unassigned)\n%s\n\n", fmtDuration(r.UnassignedSeconds))
		}
	}
}

func renderProject(p rpcapi.SummaryProject, start, end time.Time, authorEmail string, md bool) string {
	// Gather commits across all known cwd prefixes for this project.
	var allCommits []string
	for _, prefix := range p.CwdPrefixes {
		dir := filepath.Clean(prefix)
		allCommits = append(allCommits, gitLog(dir, start, end, authorEmail)...)
	}

	// Skip projects with zero hours AND zero commits.
	if p.Seconds == 0 && len(allCommits) == 0 {
		return ""
	}

	var sb strings.Builder
	if md {
		sb.WriteString(fmt.Sprintf("### %s — %s\n", p.Name, fmtDuration(p.Seconds)))
		for _, prefix := range p.CwdPrefixes {
			sb.WriteString(fmt.Sprintf("`%s`  \n", prefix))
		}
		if len(allCommits) == 0 {
			if authorEmail != "" {
				sb.WriteString("\n_no commits by you in this range_\n\n")
			} else {
				sb.WriteString("\n_no commits in this range_\n\n")
			}
		} else {
			sb.WriteString("\n")
			for _, line := range allCommits {
				sb.WriteString("- ")
				sb.WriteString(line)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString(fmt.Sprintf("  %s -- %s\n", p.Name, fmtDuration(p.Seconds)))
		for _, prefix := range p.CwdPrefixes {
			sb.WriteString(fmt.Sprintf("    %s\n", prefix))
		}
		if len(allCommits) == 0 {
			if authorEmail != "" {
				sb.WriteString("    (no commits by you in this range)\n\n")
			} else {
				sb.WriteString("    (no commits in this range)\n\n")
			}
		} else {
			for _, line := range allCommits {
				sb.WriteString("    - ")
				sb.WriteString(line)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
