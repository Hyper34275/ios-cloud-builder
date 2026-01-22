// Package workflow provides embedded GitHub Actions workflow templates
// for building iOS applications remotely.
package workflow

import (
	"embed"
	"fmt"
)

//go:embed templates/*
var templatesFS embed.FS

// GetTemplate returns the content of a template file
func GetTemplate(name string) ([]byte, error) {
	content, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return nil, fmt.Errorf("failed to read template %s: %w", name, err)
	}
	return content, nil
}

// GetWorkflowTemplate returns the iOS build workflow content
func GetWorkflowTemplate() ([]byte, error) {
	return GetTemplate("ios-build.yml")
}
