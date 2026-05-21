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
)

// ListProcessesReal shells out to `ps -A -o pid=,comm=,time=` and parses one
// row per process. CPU time is converted from MM:SS.HH (or HH:MM:SS) into
// centiseconds.
func ListProcessesReal(ctx context.Context) ([]ProcessSample, error) {
	cctx, cancel := withTimeout(ctx, psDeadline)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ps", "-A", "-o", "pid=", "-o", "comm=", "-o", "time=")
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
		fields := strings.Fields(line)
		if len(fields) < 3 {
			skipped++
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			skipped++
			continue
		}
		timeField := fields[len(fields)-1]
		comm := strings.Join(fields[1:len(fields)-1], " ")
		comm = filepath.Base(comm)

		cs, err := parseCPUTime(timeField)
		if err != nil {
			skipped++
			continue
		}
		samples = append(samples, ProcessSample{
			PID: pid, Name: comm, CPUTicks: cs,
		})
	}
	if skipped > 0 {
		// One line per call (not per row) so a permanent format change is
		// noticed but transient single-row glitches don't spam.
		log.Printf("ps: skipped %d unparseable row(s)", skipped)
	}
	return samples, scanner.Err()
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
