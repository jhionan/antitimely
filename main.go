package main

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/rian/antitimely/internal/storage"
	"github.com/rian/antitimely/internal/timer"
)

func main() {
	s, err := storage.NewStorage()
	if err != nil {
		fmt.Printf("Error initializing storage: %v\n", err)
		os.Exit(1)
	}

	data, err := s.Load()
	if err != nil {
		fmt.Printf("Error loading data: %v\n", err)
		os.Exit(1)
	}

	tm := timer.NewManager(data)

	if len(os.Args) < 2 {
		printUsage()
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "start", "resume":
		if err := tm.Start(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Session started/resumed.")
	case "pause":
		if err := tm.Pause(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Session paused.")
	case "conclude":
		if err := tm.Conclude(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Day concluded.")
	case "status":
		fmt.Printf("Status: %s\nToday's total: %v\n", tm.GetStatus(), tm.CalculateTotalDuration(tm.GetTodayKey()).Round(time.Second))
	case "report":
		printReport(tm)
	default:
		printUsage()
	}

	if err := s.Save(data); err != nil {
		fmt.Printf("Error saving data: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Antitimely - Work Timer")
	fmt.Println("Usage:")
	fmt.Println("  start | resume  Start or resume a session")
	fmt.Println("  pause           Pause the current session")
	fmt.Println("  conclude        End all sessions for today")
	fmt.Println("  status          Show current status and today's total")
	fmt.Println("  report          Show reports for all days")
}

func printReport(tm *timer.Manager) {
	fmt.Println("--- Work Report ---")
	keys := make([]string, 0, len(tm.Data.Days))
	for k := range tm.Data.Days {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		duration := tm.CalculateTotalDuration(k).Round(time.Second)
		fmt.Printf("%s: %v\n", k, duration)
	}
}
