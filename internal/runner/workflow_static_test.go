package runner_test

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/workflow"
)

func TestCentralWorkflowSecurityProperties(t *testing.T) {
	rootWorkflow, err := os.ReadFile("../../.github/workflows/ios-build.yml")
	if err != nil {
		t.Fatal(err)
	}
	template, err := workflow.GetCentralWorkflowTemplate()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rootWorkflow, template) {
		t.Fatal("root central workflow and embedded template differ")
	}
	// Git for Windows commonly checks text files out with CRLF. Normalize only
	// for semantic assertions; byte identity above still protects the embedded
	// workflow from drifting from the repository copy.
	text := strings.ReplaceAll(string(rootWorkflow), "\r\n", "\n")
	for _, required := range []string{
		"workflow_dispatch:", "permissions:\n  contents: read", "runs-on: macos-15",
		"path: builder", "path: source", "persist-credentials: false",
		"submodules: false", "lfs: false",
		"APP_CLIENT_ID", "APP_PRIVATE_KEY", "repositories: ${{ inputs.source_repo }}",
		"client-id: ${{ vars.APP_CLIENT_ID }}",
		"skip-token-revoke: true", "permission-contents: read",
		"if: always() && steps.source-token.outcome == 'success'",
		"CODE_SIGNING_ALLOWED: 'NO'", "retention-days: 1",
		"name: ios-builder-${{ inputs.build_id }}", "encrypted/build.log.age", "encrypted/App.ipa.age",
		"go mod verify",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("central workflow missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"pull_request:", "pull_request_target:", "issue_comment:", "workflow_run:",
		"actions/cache", "DerivedData cache", "use_signing", "IOS_CERTIFICATE",
		"GITHUB_STEP_SUMMARY", "eval ", "printenv", "git remote -v", "npm install",
		"app-id:", "encrypted/*.age",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("central workflow contains forbidden text %q", forbidden)
		}
	}
	uses := regexp.MustCompile(`(?m)^\s*uses:\s*[^\s@]+@([^\s]+)$`).FindAllStringSubmatch(text, -1)
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	if len(uses) == 0 {
		t.Fatal("workflow has no actions")
	}
	for _, use := range uses {
		if !sha.MatchString(use[1]) {
			t.Errorf("action is not pinned to a full SHA: %s", use[0])
		}
	}
	ordered := []string{
		"Build trusted runner before private checkout",
		"Validate all dispatch inputs before credential creation",
		"Create repository-scoped GitHub App token",
		"Checkout exactly the authorized private snapshot",
		"Revoke private repository token before project code",
		"Verify checkout credential cleanup",
		"Detect supported iOS framework",
		"Build unsigned IPA and encrypt private outputs",
		"Upload ciphertext only",
	}
	last := -1
	for _, marker := range ordered {
		index := strings.Index(text, marker)
		if index <= last {
			t.Fatalf("workflow boundary %q is missing or out of order", marker)
		}
		last = index
	}
}
