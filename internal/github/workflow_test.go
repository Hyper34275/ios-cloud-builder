package github

import (
	"context"
	"encoding/json"
	"errors"
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

func TestEnvironmentMetadataEndpoints(t *testing.T) {
	seen := map[string]bool{}
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = true
		switch r.URL.Path {
		case "/repos/o/r/environments/apple-production":
			_ = json.NewEncoder(w).Encode(Environment{ID: 1, Name: "apple-production"})
		case "/repos/o/r/environments/apple-production/deployment-branch-policies":
			if r.URL.Query().Get("per_page") != "100" {
				t.Fatalf("per_page = %q", r.URL.Query().Get("per_page"))
			}
			_ = json.NewEncoder(w).Encode(DeploymentBranchPoliciesResponse{
				TotalCount:     1,
				BranchPolicies: []DeploymentBranchPolicyEntry{{ID: 2, Name: "main", Type: "branch"}},
			})
		case "/repos/o/r/environments/apple-production/secrets/ASC_KEY_ID":
			_ = json.NewEncoder(w).Encode(ActionSecret{Name: "ASC_KEY_ID"})
		case "/repos/o/r/environments/apple-production/variables/APPLE_TEAM_ID":
			_ = json.NewEncoder(w).Encode(ActionVariable{Name: "APPLE_TEAM_ID"})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	})
	defer closeServer()
	if _, err := client.GetEnvironment(context.Background(), "o", "r", "apple-production"); err != nil {
		t.Fatal(err)
	}
	if policies, err := client.GetDeploymentBranchPolicies(context.Background(), "o", "r", "apple-production"); err != nil || len(policies) != 1 {
		t.Fatalf("GetDeploymentBranchPolicies() = %#v, %v", policies, err)
	}
	if _, err := client.GetEnvironmentActionSecret(context.Background(), "o", "r", "apple-production", "ASC_KEY_ID"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetEnvironmentActionVariable(context.Background(), "o", "r", "apple-production", "APPLE_TEAM_ID"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 4 {
		t.Fatalf("seen paths = %#v", seen)
	}
}

func TestValidateProductionEnvironment(t *testing.T) {
	valid := &Environment{
		Name: "apple-production",
		ProtectionRules: []EnvironmentRule{{
			Type:      "required_reviewers",
			Reviewers: []EnvironmentReviewer{{Type: "User"}},
		}},
		DeploymentBranchPolicy: &DeploymentBranchPolicy{CustomBranchPolicies: true},
	}
	policies := []DeploymentBranchPolicyEntry{{Name: "main", Type: "branch"}}
	if err := ValidateProductionEnvironment(valid, policies, "main"); err != nil {
		t.Fatalf("valid Environment rejected: %v", err)
	}

	tests := []struct {
		name     string
		mutate   func(*Environment)
		policies []DeploymentBranchPolicyEntry
	}{
		{name: "missing reviewers", mutate: func(environment *Environment) { environment.ProtectionRules = nil }},
		{name: "self review blocked", mutate: func(environment *Environment) { environment.ProtectionRules[0].PreventSelfReview = true }},
		{name: "protected branches instead of exact branch", mutate: func(environment *Environment) {
			environment.DeploymentBranchPolicy = &DeploymentBranchPolicy{ProtectedBranches: true}
		}},
		{name: "additional branch", policies: []DeploymentBranchPolicyEntry{{Name: "main", Type: "branch"}, {Name: "release", Type: "branch"}}},
		{name: "tag masquerading as main", policies: []DeploymentBranchPolicyEntry{{Name: "main", Type: "tag"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copyEnvironment := *valid
			copyEnvironment.ProtectionRules = append([]EnvironmentRule(nil), valid.ProtectionRules...)
			if test.mutate != nil {
				test.mutate(&copyEnvironment)
			}
			gotPolicies := policies
			if test.policies != nil {
				gotPolicies = test.policies
			}
			if err := ValidateProductionEnvironment(&copyEnvironment, gotPolicies, "main"); err == nil {
				t.Fatal("unsafe Environment was accepted")
			}
		})
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

func TestFindWorkflowRunByBuildIDMatchesWindowsTestRuns(t *testing.T) {
	buildID := "123e4567-e89b-42d3-a456-426614174000"
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(WorkflowRunsResponse{WorkflowRuns: []WorkflowRun{
			{ID: 1, DisplayTitle: "Windows Test " + buildID + "x"},
			{ID: 2, DisplayTitle: "Windows Build " + buildID},
			{ID: 3, DisplayTitle: "Windows Test " + buildID},
		}})
	})
	defer closeServer()
	run, err := client.FindWorkflowRunByBuildID(context.Background(), "builder", "public", "ios-build.yml", buildID)
	if err != nil || run.ID != 3 {
		t.Fatalf("FindWorkflowRunByBuildID() = %#v, %v", run, err)
	}
}

func TestDeleteWorkflowRun(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		body     string
		wantErr  bool
		wantBusy bool
	}{
		{"deleted", http.StatusNoContent, "", false, false},
		{"already gone", http.StatusNotFound, `{"message":"Not Found"}`, false, false},
		{"still finishing (409)", http.StatusConflict, `{"message":"Cannot delete a workflow run that is not completed"}`, true, true},
		{"still finishing (403)", http.StatusForbidden, `{"message":"Cannot delete a workflow run that is not completed"}`, true, true},
		{"server error", http.StatusBadGateway, "bad gateway", true, true},
		{"no permission", http.StatusForbidden, `{"message":"Resource not accessible by personal access token"}`, true, false},
		{"unauthorized", http.StatusUnauthorized, `{"message":"Bad credentials"}`, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var gotMethod, gotPath string
			client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			})
			defer closeServer()
			err := client.DeleteWorkflowRun(context.Background(), "builder", "public", 42)
			if gotMethod != http.MethodDelete || gotPath != "/repos/builder/public/actions/runs/42" {
				t.Fatalf("request = %s %s", gotMethod, gotPath)
			}
			if (err != nil) != test.wantErr || errors.Is(err, ErrWorkflowRunBusy) != test.wantBusy {
				t.Fatalf("DeleteWorkflowRun() = %v; want error %v, busy %v", err, test.wantErr, test.wantBusy)
			}
			if test.wantErr && test.body != "" && !strings.Contains(err.Error(), strings.Trim(strings.TrimPrefix(test.body, `{"message":`), `"}`)) {
				t.Fatalf("error %q does not carry GitHub's message", err)
			}
		})
	}
}

func TestDownloadArtifactToStreamsWithinTheLimit(t *testing.T) {
	payload := strings.Repeat("z", 1<<20)
	client, closeServer := workflowTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/artifacts/7/zip" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, payload)
	})
	defer closeServer()
	var sink strings.Builder
	var progressCalls int
	written, err := client.DownloadArtifactTo(context.Background(), "o", "r", 7, int64(len(payload)), &sink, func(int64, int64) { progressCalls++ })
	if err != nil || written != int64(len(payload)) || sink.String() != payload || progressCalls == 0 {
		t.Fatalf("DownloadArtifactTo() = %d, %v (progress %d)", written, err, progressCalls)
	}
	if _, err := client.DownloadArtifactTo(context.Background(), "o", "r", 7, int64(len(payload))-1, io.Discard, nil); err == nil {
		t.Fatal("an archive over the limit was accepted")
	}
}
