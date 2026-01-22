// Package config handles builder.json configuration file management.
// It provides loading, saving, and validation of project configuration.
package config

import (
	"encoding/json"
	"errors"
	"os"
)

// ConfigFileName is the default configuration file name.
const ConfigFileName = "builder.json"

// ErrConfigNotFound indicates builder.json was not found.
var ErrConfigNotFound = errors.New("builder.json not found")

// Manager handles configuration loading and persistence
type Manager struct {
	path string
}

// NewManager creates a new configuration manager
func NewManager() *Manager {
	return &Manager{
		path: ConfigFileName,
	}
}

// Load reads builder.json from the configured path
func (m *Manager) Load() (*Config, error) {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrConfigNotFound
		}
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Save writes configuration to builder.json
func (m *Manager) Save(cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(m.path, data, 0644)
}
