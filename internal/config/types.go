package config

// Config represents the builder.json configuration file
type Config struct {
	Project  string        `json:"project"`
	Platform string        `json:"platform"`
	GitHub   GitHubConfig  `json:"github"`
	IOS      IOSConfig     `json:"ios,omitempty"`
	Flutter  FlutterConfig `json:"flutter,omitempty"`
	MobAI    MobAIConfig   `json:"mobai,omitempty"`
}

// FlutterConfig holds Flutter-specific settings
type FlutterConfig struct {
	Version string `json:"version,omitempty"` // Pinned Flutter version (e.g., "3.24.0")
}

// IOSConfig holds iOS build settings
type IOSConfig struct {
	// Path to iOS project relative to repo root (e.g., "ios" for React Native, "platforms/ios" for Cordova)
	// Empty means root directory contains the Xcode project
	Path          string `json:"path,omitempty"`
	Scheme        string `json:"scheme,omitempty"`        // Xcode scheme to build (auto-detected if empty)
	Signing       bool   `json:"signing,omitempty"`       // Whether code signing is configured
	Configuration string `json:"configuration,omitempty"` // Build configuration: Debug (faster) or Release (production)
}

// MobAIConfig holds MobAI settings for local development
type MobAIConfig struct {
	URL      string `json:"url,omitempty"`       // MobAI API URL (default: http://localhost:8686)
	DeviceID string `json:"device_id,omitempty"` // Preferred device ID (default: first available)
}

// GitHubConfig holds GitHub repository settings
type GitHubConfig struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.Project == "" {
		return &ValidationError{Field: "project", Message: "project name is required"}
	}
	if c.GitHub.Owner == "" {
		return &ValidationError{Field: "github.owner", Message: "GitHub owner is required"}
	}
	if c.GitHub.Repo == "" {
		return &ValidationError{Field: "github.repo", Message: "GitHub repo is required"}
	}
	return nil
}

// ValidationError represents a configuration validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return "config validation error: " + e.Field + ": " + e.Message
}
