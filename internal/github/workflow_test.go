package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFindWorkflowRunByBuildIDUsesExactToken(t *testing.T) {
	buildID := "0192f819-2c07-7c9d-a9ba-0242ac120002"
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/builder/public/actions/workflows/ios-build.yml/runs" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(WorkflowRunsResponse{WorkflowRuns: []WorkflowRun{
			{ID: 1, DisplayTitle: "iOS Build " + buildID + "-attacker"},
			{ID: 2, Name: "iOS Build " + buildID, DisplayTitle: "Unrelated dispatch"},
			{ID: 4, DisplayTitle: "Attacker Build " + buildID},
			{ID: 3, DisplayTitle: "iOS Build " + buildID},
		}})
	})
	defer closeServer()

	run, err := client.FindWorkflowRunByBuildID(context.Background(), "builder", "public", "ios-build.yml", buildID)
	if err != nil {
		t.Fatalf("FindWorkflowRunByBuildID() error = %v", err)
	}
	if run.ID != 3 {
		t.Fatalf("run ID = %d, want 3", run.ID)
	}
}

func TestFindWorkflowRunByBuildIDUsesExactShareRunName(t *testing.T) {
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(WorkflowRunsResponse{WorkflowRuns: []WorkflowRun{
			{ID: 1, DisplayTitle: "iOS Build id"},
			{ID: 2, DisplayTitle: "iOS Simulator id"},
		}})
	})
	defer closeServer()
	run, err := client.FindWorkflowRunByBuildID(context.Background(), "o", "r", "ios-share.yml", "id")
	if err != nil || run.ID != 2 {
		t.Fatalf("FindWorkflowRunByBuildID() = %#v, %v", run, err)
	}
}

func TestFindWorkflowRunByBuildIDRejectsDuplicates(t *testing.T) {
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(WorkflowRunsResponse{WorkflowRuns: []WorkflowRun{
			{ID: 1, DisplayTitle: "iOS Build id"},
			{ID: 2, DisplayTitle: "iOS Build id"},
		}})
	})
	defer closeServer()
	if _, err := client.FindWorkflowRunByBuildID(context.Background(), "o", "r", "w", "id"); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("FindWorkflowRunByBuildID() error = %v", err)
	}
}

func TestFindWorkflowRunByBuildIDPaginates(t *testing.T) {
	buildID := "550e8400-e29b-41d4-a716-446655440000"
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "1" {
			runs := make([]WorkflowRun, 100)
			for i := range runs {
				runs[i] = WorkflowRun{ID: int64(i + 1), DisplayTitle: fmt.Sprintf("iOS Build unrelated-%d", i)}
			}
			_ = json.NewEncoder(w).Encode(WorkflowRunsResponse{TotalCount: 101, WorkflowRuns: runs})
			return
		}
		if page != "2" {
			t.Fatalf("page = %q", page)
		}
		_ = json.NewEncoder(w).Encode(WorkflowRunsResponse{TotalCount: 101, WorkflowRuns: []WorkflowRun{{ID: 101, DisplayTitle: "iOS Build " + buildID}}})
	})
	defer closeServer()
	run, err := client.FindWorkflowRunByBuildID(context.Background(), "o", "r", "ios-build.yml", buildID)
	if err != nil || run.ID != 101 {
		t.Fatalf("FindWorkflowRunByBuildID() = %#v, %v", run, err)
	}
}

func TestFindArtifactByNameRequiresUniqueUnexpiredArtifact(t *testing.T) {
	tests := []struct {
		name      string
		artifacts []Artifact
		wantID    int64
		wantError string
	}{
		{name: "exact", artifacts: []Artifact{{ID: 1, Name: "other"}, {ID: 2, Name: "ios-builder-id"}}, wantID: 2},
		{name: "duplicate", artifacts: []Artifact{{ID: 2, Name: "ios-builder-id"}, {ID: 3, Name: "ios-builder-id"}}, wantError: "multiple"},
		{name: "expired", artifacts: []Artifact{{ID: 2, Name: "ios-builder-id", Expired: true}}, wantError: "expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/o/r/actions/runs/77/artifacts" {
					t.Fatalf("path = %q", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(ArtifactsResponse{Artifacts: tt.artifacts})
			})
			defer closeServer()
			got, err := client.FindArtifactByName(context.Background(), "o", "r", 77, "ios-builder-id")
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("FindArtifactByName() error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil || got.ID != tt.wantID {
				t.Fatalf("FindArtifactByName() = %#v, %v", got, err)
			}
		})
	}
}

func TestDownloadArtifactWithProgressLimit(t *testing.T) {
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "6")
		_, _ = io.WriteString(w, "123456")
	})
	defer closeServer()
	if _, err := client.DownloadArtifactWithProgressLimit(context.Background(), "o", "r", 1, 5, nil); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("DownloadArtifactWithProgressLimit() error = %v", err)
	}
}

func TestDeleteArtifact(t *testing.T) {
	var method, path string
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	defer closeServer()
	if err := client.DeleteArtifact(context.Background(), "owner", "repo", 42); err != nil {
		t.Fatalf("DeleteArtifact() error = %v", err)
	}
	if method != http.MethodDelete || path != "/repos/owner/repo/actions/artifacts/42" {
		t.Fatalf("request = %s %s", method, path)
	}
}

func TestTriggerWorkflowDispatchPayload(t *testing.T) {
	wantInputs := map[string]string{"build_id": "id", "source_repo": "private"}
	var got WorkflowDispatchRequest
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/builder/public":
			_ = json.NewEncoder(w).Encode(Repository{DefaultRef: "trunk"})
		case "POST /repos/builder/public/actions/workflows/ios-build.yml/dispatches":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	defer closeServer()
	if err := client.TriggerWorkflow(context.Background(), "builder", "public", "ios-build.yml", wantInputs); err != nil {
		t.Fatalf("TriggerWorkflow() error = %v", err)
	}
	if got.Ref != "trunk" || !reflect.DeepEqual(got.Inputs, wantInputs) {
		t.Fatalf("dispatch = %#v", got)
	}
}

func TestPollForWorkflowCompletionUsesExactRunID(t *testing.T) {
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/runs/123" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(WorkflowRun{ID: 123, Status: "completed", Conclusion: "failure"})
	})
	defer closeServer()
	run, err := client.PollForWorkflowCompletion(context.Background(), "o", "r", 123, time.Second, nil)
	if err != nil || run.ID != 123 || run.Conclusion != "failure" {
		t.Fatalf("PollForWorkflowCompletion() = %#v, %v", run, err)
	}
}

func workflowTestClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()
	return client, server.Close
}

func TestDownloadArtifactLimitWithoutContentLength(t *testing.T) {
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Transfer-Encoding", "chunked")
		_, _ = fmt.Fprint(w, "123456")
	})
	defer closeServer()
	if _, err := client.DownloadArtifactWithProgressLimit(context.Background(), "o", "r", 1, 5, nil); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("DownloadArtifactWithProgressLimit() error = %v", err)
	}
}
