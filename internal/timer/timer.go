package timer

import (
	"fmt"
	"time"

	"github.com/rian/antitimely/internal/models"
)

type Manager struct {
	Data *models.StorageData
}

func NewManager(data *models.StorageData) *Manager {
	return &Manager{Data: data}
}

func (m *Manager) GetTodayKey() string {
	return time.Now().Format("2006-01-02")
}

func (m *Manager) GetTodayData() *models.DayData {
	key := m.GetTodayKey()
	if _, ok := m.Data.Days[key]; !ok {
		m.Data.Days[key] = &models.DayData{Sessions: []models.Session{}}
	}
	return m.Data.Days[key]
}

func (m *Manager) Start() error {
	today := m.GetTodayData()
	if len(today.Sessions) > 0 {
		last := today.Sessions[len(today.Sessions)-1]
		if last.EndTime == nil {
			return fmt.Errorf("a session is already running")
		}
	}

	today.Sessions = append(today.Sessions, models.Session{
		StartTime: time.Now(),
	})
	return nil
}

func (m *Manager) Pause() error {
	today := m.GetTodayData()
	if len(today.Sessions) == 0 {
		return fmt.Errorf("no session running to pause")
	}

	last := &today.Sessions[len(today.Sessions)-1]
	if last.EndTime != nil {
		return fmt.Errorf("no session running to pause")
	}

	now := time.Now()
	last.EndTime = &now
	return nil
}

func (m *Manager) Conclude() error {
	today := m.GetTodayData()
	if len(today.Sessions) > 0 {
		last := &today.Sessions[len(today.Sessions)-1]
		if last.EndTime == nil {
			now := time.Now()
			last.EndTime = &now
		}
	}
	return nil
}

func (m *Manager) GetStatus() string {
	today := m.GetTodayData()
	if len(today.Sessions) == 0 {
		return "Stopped"
	}

	last := today.Sessions[len(today.Sessions)-1]
	if last.EndTime == nil {
		return fmt.Sprintf("Running (started %s)", last.StartTime.Format("15:04:05"))
	}
	return "Paused"
}

func (m *Manager) CalculateTotalDuration(dayKey string) time.Duration {
	day, ok := m.Data.Days[dayKey]
	if !ok {
		return 0
	}

	var total time.Duration
	for _, s := range day.Sessions {
		if s.EndTime != nil {
			total += s.EndTime.Sub(s.StartTime)
		} else {
			total += time.Since(s.StartTime)
		}
	}
	return total
}
