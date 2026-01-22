// Package build provides build coordination for remote iOS builds via GitHub Actions.
// It handles triggering workflows, monitoring progress, and downloading artifacts.
package build

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/google/uuid"
)

const (
	// DefaultTimeout is the default build timeout
	DefaultTimeout = 30 * time.Minute

	// WorkflowFile is the name of the workflow file
	WorkflowFile = "ios-build.yml"

	// IPAArtifactName is the name of the IPA artifact uploaded by the workflow
	IPAArtifactName = "ipa"
)

// Coordinator orchestrates the build process
type Coordinator struct {
	config   *config.Config
	github   *github.Client
	progress *Progress
}

// NewCoordinator creates a new build coordinator
func NewCoordinator(cfg *config.Config, gh *github.Client) *Coordinator {
	return &Coordinator{
		config:   cfg,
		github:   gh,
		progress: NewProgress(os.Stdout),
	}
}

// BuildOptions contains options for a build
type BuildOptions struct {
	OutputDir string
	Timeout   time.Duration
	Unsigned  bool // Skip code signing even if configured
}

// BuildResult contains the result of a build
type BuildResult struct {
	BuildID     string
	IPAPath     string
	Duration    time.Duration
	WorkflowURL string
	IPASize     int64
}

// Build triggers a remote build and downloads the IPA artifact
func (c *Coordinator) Build(ctx context.Context, opts BuildOptions) (*BuildResult, error) {
	startTime := time.Now()

	// Set default timeout
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	// Generate build ID
	buildID := uuid.New().String()[:8]
	c.progress.Start(buildID)

	// Step 1: Trigger workflow
	c.progress.Update(PhaseTriggering, "Triggering GitHub Actions build...")
	triggerTime := time.Now()
	inputs := map[string]string{
		"build_id": buildID,
	}
	// Add iOS-specific inputs if configured
	if c.config.IOS.Path != "" {
		inputs["ios_path"] = c.config.IOS.Path
	}
	if c.config.IOS.Scheme != "" {
		inputs["scheme"] = c.config.IOS.Scheme
	}
	// Determine signing: use signing if configured and not explicitly disabled
	useSigning := c.config.IOS.Signing && !opts.Unsigned
	if useSigning {
		inputs["use_signing"] = "true"
	}
	// Pass build configuration (Debug is faster, Release for production)
	if c.config.IOS.Configuration != "" {
		inputs["configuration"] = c.config.IOS.Configuration
	}
	// Pass Flutter version if configured (ensures SDK version match for hot reload)
	if c.config.Flutter.Version != "" {
		inputs["flutter_version"] = c.config.Flutter.Version
	}
	if err := c.github.TriggerWorkflow(ctx, c.config.GitHub.Owner, c.config.GitHub.Repo, WorkflowFile, inputs); err != nil {
		c.progress.Error(PhaseTriggering, err)
		return nil, fmt.Errorf("failed to trigger workflow: %w", err)
	}
	c.progress.Complete(PhaseTriggering, "Workflow triggered")

	// Step 2: Wait for workflow to start
	c.progress.Update(PhaseWaitingStart, "Waiting for workflow to start...")
	run, err := c.github.PollForWorkflowStart(ctx, c.config.GitHub.Owner, c.config.GitHub.Repo, WorkflowFile, triggerTime, 2*time.Minute)
	if err != nil {
		c.progress.Error(PhaseWaitingStart, err)
		return nil, fmt.Errorf("workflow failed to start: %w", err)
	}
	c.progress.Complete(PhaseWaitingStart, fmt.Sprintf("Workflow started (run #%d)", run.ID))

	// Step 3: Wait for IPA artifact (don't wait for full job completion)
	c.progress.Update(PhaseBuilding, "Building... (this may take several minutes)")
	c.progress.SetWorkflowURL(run.HTMLURL)

	// Poll for artifact availability instead of waiting for job completion
	// This allows us to download the IPA as soon as it's uploaded, without waiting
	// for cache save and other post-build steps
	artifact, err := c.github.PollForArtifact(ctx, c.config.GitHub.Owner, c.config.GitHub.Repo, run.ID, IPAArtifactName, opts.Timeout)
	if err != nil {
		c.progress.Error(PhaseBuilding, err)
		return nil, fmt.Errorf("build failed: %w", err)
	}
	c.progress.Complete(PhaseBuilding, "Build completed successfully")

	// Step 4: Download IPA artifact immediately
	c.progress.Update(PhaseDownloading, "Downloading IPA artifact...")
	ipaPath, ipaSize, err := c.downloadIPAArtifactByID(ctx, opts.OutputDir, artifact.ID, buildID)
	if err != nil {
		c.progress.Error(PhaseDownloading, err)
		return nil, fmt.Errorf("failed to download artifact: %w", err)
	}
	c.progress.Complete(PhaseDownloading, fmt.Sprintf("IPA downloaded (%.2f MB)", float64(ipaSize)/(1024*1024)))

	// Check if we downloaded before job completed (v4 artifact early access)
	jobRun, err := c.github.GetWorkflowRun(ctx, c.config.GitHub.Owner, c.config.GitHub.Repo, run.ID)
	if err == nil {
		if jobRun.Status != "completed" {
			c.progress.Update(PhaseDownloading, fmt.Sprintf("✨ Downloaded while job still running (status: %s)", jobRun.Status))
		} else {
			c.progress.Update(PhaseDownloading, fmt.Sprintf("Job already completed (status: %s)", jobRun.Status))
		}
	}

	c.progress.Finish()

	return &BuildResult{
		BuildID:     buildID,
		IPAPath:     ipaPath,
		Duration:    time.Since(startTime),
		WorkflowURL: run.HTMLURL,
		IPASize:     ipaSize,
	}, nil
}

// downloadIPAArtifactByID downloads an artifact by its ID
func (c *Coordinator) downloadIPAArtifactByID(ctx context.Context, outputDir string, artifactID int64, buildID string) (string, int64, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", 0, fmt.Errorf("failed to create output directory: %w", err)
	}

	// Download the artifact (comes as a ZIP) with progress
	artifactZip, err := c.github.DownloadArtifactWithProgress(ctx, c.config.GitHub.Owner, c.config.GitHub.Repo, artifactID, func(downloaded, total int64) {
		c.progress.UpdateDownloadProgress(downloaded, total)
	})
	if err != nil {
		return "", 0, fmt.Errorf("failed to download artifact: %w", err)
	}

	// Extract IPA from the artifact ZIP
	ipaPath := filepath.Join(outputDir, fmt.Sprintf("%s-%s.ipa", c.config.Project, buildID))
	ipaSize, err := extractIPAFromZip(artifactZip, ipaPath)
	if err != nil {
		return "", 0, fmt.Errorf("failed to extract IPA: %w", err)
	}

	return ipaPath, ipaSize, nil
}

// extractIPAFromZip extracts the IPA file from an artifact ZIP
func extractIPAFromZip(zipData []byte, destPath string) (int64, error) {
	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return 0, fmt.Errorf("failed to open artifact zip: %w", err)
	}

	for _, f := range reader.File {
		if filepath.Ext(f.Name) == ".ipa" {
			size, err := extractFile(f, destPath)
			if err != nil {
				return 0, err
			}
			return size, nil
		}
	}

	return 0, fmt.Errorf("no IPA file found in artifact")
}

func extractFile(f *zip.File, destPath string) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("failed to open IPA in zip: %w", err)
	}
	defer func() { _ = rc.Close() }()

	out, err := os.Create(destPath)
	if err != nil {
		return 0, fmt.Errorf("failed to create output file: %w", err)
	}
	defer func() { _ = out.Close() }()

	size, err := io.Copy(out, rc)
	if err != nil {
		return 0, fmt.Errorf("failed to write IPA: %w", err)
	}

	return size, nil
}
