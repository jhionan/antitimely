package storage

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/rian/antitimely/internal/models"
)

type Storage struct {
	FilePath string
}

func NewStorage() (*Storage, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".antitimely")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &Storage{
		FilePath: filepath.Join(dir, "data.json"),
	}, nil
}

func (s *Storage) Load() (*models.StorageData, error) {
	if _, err := os.Stat(s.FilePath); os.IsNotExist(err) {
		return &models.StorageData{Days: make(map[string]*models.DayData)}, nil
	}

	data, err := os.ReadFile(s.FilePath)
	if err != nil {
		return nil, err
	}

	var storageData models.StorageData
	if err := json.Unmarshal(data, &storageData); err != nil {
		return nil, err
	}
	if storageData.Days == nil {
		storageData.Days = make(map[string]*models.DayData)
	}
	return &storageData, nil
}

func (s *Storage) Save(data *models.StorageData) error {
	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.FilePath, bytes, 0644)
}
