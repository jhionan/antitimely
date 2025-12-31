package models

import "time"

type Session struct {
	StartTime time.Time  `json:"start_time"`
	EndTime   *time.Time `json:"end_time,omitempty"`
}

type DayData struct {
	Sessions []Session `json:"sessions"`
}

type StorageData struct {
	Days map[string]*DayData `json:"days"` // Key: YYYY-MM-DD
}
