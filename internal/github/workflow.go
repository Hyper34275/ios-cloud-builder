package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// workflowStartPollInterval is the polling interval when waiting for workflow to start.
	workflowStartPollInterval = 2 * time.Second
)

// TriggerWorkflow triggers a workflow_dispatch event
func (c *Client) TriggerWorkflow(ctx context.Context, owner, repo, workflowFile string, inputs map[string]string) error {
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/dispatches", owner, repo, workflowFile)

	// Get the default branch
	repository, err := c.GetRepository(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("failed to get repository: %w", err)
	}

	ref := repository.DefaultRef
	if ref == "" {
		ref = "main"
	}

	req := WorkflowDispatchRequest{
		Ref:    ref,
		Inputs: inputs,
	}

	resp, err := c.request(ctx, "POST", path, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to trigger workflow: status %d", resp.StatusCode)
	}

	return nil
}

// GetWorkflowRun retrieves a specific workflow run
func (c *Client) GetWorkflowRun(ctx context.Context, owner, repo string, runID int64) (*WorkflowRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d", owner, repo, runID)

	var run WorkflowRun
	if err := c.do(ctx, path, &run); err != nil {
		return nil, fmt.Errorf("failed to get workflow run: %w", err)
	}

	return &run, nil
}

// ListWorkflowRuns lists workflow runs for a workflow file
func (c *Client) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]WorkflowRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/runs?per_page=10", owner, repo, workflowFile)

	var resp WorkflowRunsResponse
	if err := c.do(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("failed to list workflow runs: %w", err)
	}

	return resp.WorkflowRuns, nil
}

// FindLatestWorkflowRun finds the most recent workflow run created after a given time
func (c *Client) FindLatestWorkflowRun(ctx context.Context, owner, repo, workflowFile string, after time.Time) (*WorkflowRun, error) {
	runs, err := c.ListWorkflowRuns(ctx, owner, repo, workflowFile)
	if err != nil {
		return nil, err
	}

	for i := range runs {
		if runs[i].CreatedAt.After(after) {
			return &runs[i], nil
		}
	}

	return nil, fmt.Errorf("no workflow run found after %v", after)
}

// PollForWorkflowStart polls until a new workflow run appears after triggerTime
func (c *Client) PollForWorkflowStart(ctx context.Context, owner, repo, workflowFile string, triggerTime time.Time, timeout time.Duration) (*WorkflowRun, error) {
	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for workflow to start")
		}

		run, err := c.FindLatestWorkflowRun(ctx, owner, repo, workflowFile, triggerTime)
		if err == nil {
			return run, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(workflowStartPollInterval):
		}
	}
}

// ListRunArtifacts lists all artifacts for a workflow run
func (c *Client) ListRunArtifacts(ctx context.Context, owner, repo string, runID int64) ([]Artifact, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/artifacts", owner, repo, runID)

	var resp ArtifactsResponse
	if err := c.do(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("failed to list artifacts: %w", err)
	}

	return resp.Artifacts, nil
}

// ProgressFunc is called during download with bytes downloaded and total size
type ProgressFunc func(downloaded, total int64)

// DownloadArtifactWithProgress downloads an artifact with progress reporting
func (c *Client) DownloadArtifactWithProgress(ctx context.Context, owner, repo string, artifactID int64, progress ProgressFunc) ([]byte, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/artifacts/%d/zip", owner, repo, artifactID)

	resp, err := c.request(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusFound {
		// Close initial response before following redirect
		resp.Body.Close()

		redirectURL := resp.Header.Get("Location")
		if redirectURL == "" {
			return nil, fmt.Errorf("artifact redirect missing Location header")
		}

		req, err := http.NewRequestWithContext(ctx, "GET", redirectURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download artifact: status %d", resp.StatusCode)
	}

	total := resp.ContentLength

	// Read with progress tracking
	var data []byte
	if progress != nil && total > 0 {
		data = make([]byte, 0, total)
		buf := make([]byte, 32*1024) // 32KB buffer
		var downloaded int64
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				data = append(data, buf[:n]...)
				downloaded += int64(n)
				progress(downloaded, total)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("failed to read artifact data: %w", err)
			}
		}
	} else {
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read artifact data: %w", err)
		}
	}

	return data, nil
}

// FindArtifactByName finds an artifact by name in a workflow run
func (c *Client) FindArtifactByName(ctx context.Context, owner, repo string, runID int64, name string) (*Artifact, error) {
	artifacts, err := c.ListRunArtifacts(ctx, owner, repo, runID)
	if err != nil {
		return nil, err
	}

	for _, a := range artifacts {
		if a.Name == name {
			return &a, nil
		}
	}

	return nil, fmt.Errorf("artifact %q not found", name)
}

// PollForArtifact polls until an artifact with the given name appears in a workflow run.
// This allows downloading the artifact as soon as it's uploaded, without waiting for the
// entire workflow to complete.
func (c *Client) PollForArtifact(ctx context.Context, owner, repo string, runID int64, artifactName string, timeout time.Duration) (*Artifact, error) {
	deadline := time.Now().Add(timeout)
	// Use fixed 5s interval (no backoff) to catch artifact quickly after upload
	const artifactPollInterval = 5 * time.Second

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for artifact %q", artifactName)
		}

		// Check if artifact is available
		artifact, err := c.FindArtifactByName(ctx, owner, repo, runID, artifactName)
		if err == nil {
			return artifact, nil
		}

		// Check if workflow failed (no point waiting for artifact)
		run, err := c.GetWorkflowRun(ctx, owner, repo, runID)
		if err != nil {
			return nil, fmt.Errorf("failed to check workflow status: %w", err)
		}
		if run.Status == "completed" && run.Conclusion != "success" {
			return nil, fmt.Errorf("workflow failed with conclusion: %s", run.Conclusion)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(artifactPollInterval):
			// Fixed interval - no backoff to catch artifact quickly
		}
	}
}
