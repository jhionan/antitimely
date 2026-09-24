package macos

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ListProcessesReal shells out to `ps -A -o pid=,time=,comm=` and parses one
// row per process. CPU time is converted from MM:SS.HH (or HH:MM:SS) into
// centiseconds.
//
// comm must stay the LAST column: ps pads every earlier column to a fixed
// width and truncates comm there to 16 bytes, which cut the Claude desktop
// app's full executable path down to "/Users/rian/Libr".
func ListProcessesReal(ctx context.Context) ([]ProcessSample, error) {
	cctx, cancel := withTimeout(ctx, psDeadline)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ps", "-A", "-o", "pid=", "-o", "time=", "-o", "comm=")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}

	var (
		samples []ProcessSample
		skipped int
	)
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		sample, ok := parsePSLine(line)
		if !ok {
			skipped++
			continue
		}
		samples = append(samples, sample)
	}
	if skipped > 0 {
		// One line per call (not per row) so a permanent format change is
		// noticed but transient single-row glitches don't spam.
		log.Printf("ps: skipped %d unparseable row(s)", skipped)
	}
	return samples, scanner.Err()
}

// parsePSLine parses one "pid time comm" row. comm is everything after the
// time column, kept verbatim because executable paths contain spaces
// ("Application Support").
func parsePSLine(line string) (ProcessSample, bool) {
	pidField, rest := cutField(line)
	timeField, comm := cutField(rest)
	if comm == "" {
		return ProcessSample{}, false
	}
	pid, err := strconv.Atoi(pidField)
	if err != nil {
		return ProcessSample{}, false
	}
	cs, err := parseCPUTime(timeField)
	if err != nil {
		return ProcessSample{}, false
	}
	return ProcessSample{PID: pid, Name: binaryName(comm), CPUTicks: cs}, true
}

// cutField splits off the first whitespace-delimited field of s.
func cutField(s string) (field, rest string) {
	s = strings.TrimLeft(s, " \t")
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}

// maxTitleLen caps a process title used as a binary name: long enough to still
// read what the process is doing ("ng test --watch=false --browsers=..."),
// short enough that one agent's sprawling command line can't bloat the
// observation key.
const maxTitleLen = 64

// binaryName turns ps's comm into the name the allowlist, observations and
// rules key on. comm is either an executable path
// ("/opt/homebrew/Cellar/dotnet/10.0.400/libexec/dotnet") or a process title
// a program set on itself ("ng serve --port 4200",
// "npm exec @playwright/mcp@latest").
//
// Only a path is reduced to its base name: running filepath.Base over a title
// turns "npm exec @playwright/mcp@latest" into "mcp@latest". A title is kept
// whole, first word included, so "ng serve" and "ng test" stay distinct.
func binaryName(comm string) string {
	if strings.HasPrefix(comm, "/") || strings.HasPrefix(comm, "./") || strings.HasPrefix(comm, "../") {
		return filepath.Base(comm)
	}
	if len(comm) <= maxTitleLen {
		return comm
	}
	cut := maxTitleLen
	for cut > 0 && !utf8.RuneStart(comm[cut]) {
		cut-- // back off to a rune boundary; never split a multi-byte char
	}
	return strings.TrimSpace(comm[:cut])
}

// parseCPUTime parses ps's TIME output (e.g. "0:01.23" or "1:23:45") into
// centiseconds.
func parseCPUTime(s string) (uint64, error) {
	parts := strings.Split(s, ":")
	var hours, minutes uint64
	var secStr string
	switch len(parts) {
	case 2:
		m, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			return 0, err
		}
		minutes = m
		secStr = parts[1]
	case 3:
		h, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			return 0, err
		}
		hours = h
		m, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return 0, err
		}
		minutes = m
		secStr = parts[2]
	default:
		return 0, fmt.Errorf("unexpected format: %q", s)
	}
	secParts := strings.Split(secStr, ".")
	sec, err := strconv.ParseUint(secParts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	var centi uint64
	if len(secParts) == 2 {
		f := secParts[1]
		if len(f) > 2 {
			f = f[:2]
		}
		for len(f) < 2 {
			f += "0"
		}
		centi, err = strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, err
		}
	}
	total := hours*360000 + minutes*6000 + sec*100 + centi
	return total, nil
}
