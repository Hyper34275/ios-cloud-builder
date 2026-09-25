package runner_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/workflow"
	"go.yaml.in/yaml/v3"
)

func TestPublicWorkflowsDisableSetupGoCaches(t *testing.T) {
	t.Parallel()

	var workflowPaths []string
	for _, pattern := range []string{"../../.github/workflows/*.yml", "../../.github/workflows/*.yaml"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob public workflows with %q: %v", pattern, err)
		}
		workflowPaths = append(workflowPaths, matches...)
	}
	if len(workflowPaths) == 0 {
		t.Fatal("no public workflows found")
	}

	for _, workflowPath := range workflowPaths {
		contents, readErr := os.ReadFile(workflowPath)
		if readErr != nil {
			t.Fatalf("read %s: %v", workflowPath, readErr)
		}
		lines := strings.Split(string(contents), "\n")
		for index, line := range lines {
			if !strings.Contains(line, "uses: actions/setup-go@") {
				continue
			}

			stepIndent := len(line) - len(strings.TrimLeft(line, " "))
			cacheDisabled := false
			for next := index + 1; next < len(lines); next++ {
				trimmed := strings.TrimSpace(lines[next])
				indent := len(lines[next]) - len(strings.TrimLeft(lines[next], " "))
				if strings.HasPrefix(trimmed, "- ") && indent <= stepIndent {
					break
				}
				if trimmed == "cache: false" {
					cacheDisabled = true
					break
				}
			}
			if !cacheDisabled {
				t.Errorf("%s:%d setup-go must explicitly set cache: false", workflowPath, index+1)
			}
		}
	}
}

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
	var parsed any
	if err := yaml.Unmarshal(rootWorkflow, &parsed); err != nil {
		t.Fatalf("central workflow is not valid YAML: %v", err)
	}
	// Git for Windows commonly checks text files out with CRLF. Normalize only
	// for semantic assertions; byte identity above still protects the embedded
	// workflow from drifting from the repository copy.
	text := strings.ReplaceAll(string(rootWorkflow), "\r\n", "\n")
	for _, required := range []string{
		"workflow_dispatch:", "permissions:\n  contents: read", "runs-on: macos-15",
		"path: builder", "path: source", "persist-credentials: false",
		"Set up trusted Go toolchain", "go-version-file: builder/go.mod", "cache: false",
		"submodules: false", "lfs: false",
		"APP_CLIENT_ID", "APP_PRIVATE_KEY", "repositories: ${{ inputs.source_repo }}",
		"client-id: ${{ vars.APP_CLIENT_ID }}",
		"skip-token-revoke: true", "permission-contents: read",
		"if: always() && steps.source-token.outcome == 'success'",
		"CODE_SIGNING_ALLOWED: 'NO'", "retention-days: 1",
		"name: ios-builder-${{ inputs.build_id }}", "encrypted/build.log.age", "encrypted/App.ipa.age",
		"go mod verify",
		"operation:", "environment: apple-production", "APPLE_SIGNING_RECIPIENT",
		"APPLE_SIGNING_AGE_IDENTITY", "APPLE_DISTRIBUTION_P12", "APPLE_PROVISIONING_PROFILE", "APPLE_PROVISIONING_PROFILES",
		"ASC_API_KEY_P8", "deploy-testflight", "--build-number", "needs.build.outputs.build_number", "github.run_attempt", "ios-builder-deploy-${{ inputs.build_id }}",
		"runs-on: macos-26", "test_script:", "execute-tests", "encrypted/test.log.age", "encrypted/report.md.age",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("central workflow missing %q", required)
		}
	}
	deployJob := jobBlock(t, text, "sign-and-deploy")
	for _, forbidden := range []string{
		"path: source", "APP_PRIVATE_KEY", "source-token", "source_owner", "source_repo",
		"App.ipa\n", "App.ipa.age\n          retention-days",
	} {
		if strings.Contains(deployJob, forbidden) {
			t.Errorf("protected deployment job contains forbidden text %q", forbidden)
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
		"Set up trusted Go toolchain",
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

// jobBlock returns the text of one top-level job, from its key to the next
// job's key, so per-job assertions cannot see a later job.
func jobBlock(t *testing.T, text, job string) string {
	t.Helper()
	start := strings.Index(text, "\n  "+job+":\n")
	if start < 0 {
		t.Fatalf("central workflow has no %s job", job)
	}
	block := text[start+1:]
	if next := regexp.MustCompile(`\n  [A-Za-z0-9_-]+:\n`).FindStringIndex(block[1:]); next != nil {
		block = block[:next[0]+1]
	}
	return block
}

// The structs below decode the central workflow strictly: an unknown key
// anywhere fails the test, so a new capability (permissions, services,
// containers, secrets: inherit, ...) cannot enter a job without these checks
// being revisited.
type staticWorkflow struct {
	Name        string               `yaml:"name"`
	RunName     string               `yaml:"run-name"`
	On          staticTriggers       `yaml:"on"`
	Permissions map[string]string    `yaml:"permissions"`
	Jobs        map[string]staticJob `yaml:"jobs"`
}

type staticTriggers struct {
	WorkflowDispatch struct {
		Inputs map[string]staticInput `yaml:"inputs"`
	} `yaml:"workflow_dispatch"`
}

type staticInput struct {
	Description string   `yaml:"description"`
	Required    bool     `yaml:"required"`
	Default     string   `yaml:"default"`
	Type        string   `yaml:"type"`
	Options     []string `yaml:"options"`
}

type staticJob struct {
	If             string            `yaml:"if"`
	Needs          string            `yaml:"needs"`
	Environment    string            `yaml:"environment"`
	RunsOn         string            `yaml:"runs-on"`
	TimeoutMinutes int               `yaml:"timeout-minutes"`
	Outputs        map[string]string `yaml:"outputs"`
	Env            map[string]string `yaml:"env"`
	Steps          []staticStep      `yaml:"steps"`
}

type staticStep struct {
	Name             string            `yaml:"name"`
	ID               string            `yaml:"id"`
	If               string            `yaml:"if"`
	Uses             string            `yaml:"uses"`
	Run              string            `yaml:"run"`
	WorkingDirectory string            `yaml:"working-directory"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	Env              map[string]string `yaml:"env"`
	With             map[string]string `yaml:"with"`
}

func loadStaticWorkflow(t *testing.T) staticWorkflow {
	t.Helper()
	contents, err := os.ReadFile("../../.github/workflows/ios-build.yml")
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var parsed staticWorkflow
	if err := decoder.Decode(&parsed); err != nil {
		t.Fatalf("central workflow has an unexpected shape: %v", err)
	}
	return parsed
}

func stepIndex(job *staticJob, name string) int {
	for index, step := range job.Steps {
		if step.Name == name {
			return index
		}
	}
	return -1
}

func stepText(t *testing.T, step *staticStep) string {
	t.Helper()
	data, err := yaml.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCentralWorkflowRunScriptsNeverInterpolateExpressions(t *testing.T) {
	workflow := loadStaticWorkflow(t)
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			// Expressions are expanded into the script text before bash parses
			// it, so an input there is shell injection. Inputs reach scripts
			// only through env.
			if strings.Contains(step.Run, "${{") {
				t.Errorf("%s / %s interpolates an expression into its script", jobName, step.Name)
			}
		}
	}
}

func TestCentralWorkflowTestOperation(t *testing.T) {
	workflow := loadStaticWorkflow(t)
	inputs := workflow.On.WorkflowDispatch.Inputs
	if got := inputs["operation"].Options; !reflect.DeepEqual(got, []string{"build", "testflight", "test"}) {
		t.Errorf("operation options = %v", got)
	}
	if script := inputs["test_script"]; script.Required || script.Default != "" || script.Type != "string" {
		t.Errorf("test_script input = %+v, want an optional string defaulting to empty", script)
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("workflow permissions = %v", workflow.Permissions)
	}
	jobNames := make([]string, 0, len(workflow.Jobs))
	for name := range workflow.Jobs {
		jobNames = append(jobNames, name)
	}
	sort.Strings(jobNames)
	if !reflect.DeepEqual(jobNames, []string{"build", "sign-and-deploy", "test"}) {
		t.Fatalf("jobs = %v", jobNames)
	}
	build, deploy, test := workflow.Jobs["build"], workflow.Jobs["sign-and-deploy"], workflow.Jobs["test"]

	// Exactly one path runs per operation, and the test path never reaches the
	// protected Environment or its signing job.
	if build.If != "inputs.operation != 'test'" || test.If != "inputs.operation == 'test'" ||
		deploy.If != "inputs.operation == 'testflight'" || deploy.Needs != "build" {
		t.Errorf("job conditions: build %q, test %q, deploy %q needs %q", build.If, test.If, deploy.If, deploy.Needs)
	}
	if test.RunsOn != "macos-26" || test.TimeoutMinutes != 120 {
		t.Errorf("test job runs-on %q with timeout %d, want macos-26 and 120", test.RunsOn, test.TimeoutMinutes)
	}
	if test.Environment != "" || test.Needs != "" || len(test.Outputs) != 0 {
		t.Errorf("test job must have no Environment, dependency, or outputs: %+v", test)
	}
	if !reflect.DeepEqual(test.Env, build.Env) || !strings.HasPrefix(test.Env["DEVELOPER_DIR"], "/Applications/Xcode_26") {
		t.Errorf("test job env %v must mirror the build job's Xcode selection %v", test.Env, build.Env)
	}

	// Validation, token minting, checkout, and revocation are the build job's
	// own steps, byte for byte, in the same order.
	shared := []string{
		"Checkout trusted public builder",
		"Set up trusted Go toolchain",
		"Build trusted runner before private checkout",
		"Validate all dispatch inputs before credential creation",
		"Create repository-scoped GitHub App token",
		"Checkout exactly the authorized private snapshot",
		"Revoke private repository token before project code",
		"Verify checkout credential cleanup",
	}
	wantSteps := append(append([]string{}, shared...),
		"Run project tests and encrypt private outputs",
		"Upload ciphertext only",
		"Report private test status",
	)
	gotSteps := make([]string, 0, len(test.Steps))
	for _, step := range test.Steps {
		gotSteps = append(gotSteps, step.Name)
	}
	if !reflect.DeepEqual(gotSteps, wantSteps) {
		t.Fatalf("test job steps = %q, want %q", gotSteps, wantSteps)
	}
	for index, name := range shared {
		buildIndex := stepIndex(&build, name)
		if buildIndex < 0 || !reflect.DeepEqual(test.Steps[index], build.Steps[buildIndex]) {
			t.Errorf("test job step %q differs from the build job's", name)
		}
	}
	validate := test.Steps[stepIndex(&test, "Validate all dispatch inputs before credential creation")]
	if validate.Env["TEST_SCRIPT"] != "${{ inputs.test_script }}" || !strings.Contains(validate.Run, `--test-script "$TEST_SCRIPT"`) {
		t.Error("test_script is not validated before credential creation")
	}
	revokeIndex := stepIndex(&test, "Revoke private repository token before project code")
	runIndex := stepIndex(&test, "Run project tests and encrypt private outputs")
	if revoke := test.Steps[revokeIndex]; revokeIndex > runIndex || revoke.If != "always() && steps.source-token.outcome == 'success'" ||
		!strings.Contains(revoke.Run, "revoke-token") {
		t.Error("the private repository token is not revoked before the test script runs")
	}

	// Credentials: the App key and client ID exist only to mint the token,
	// the token only reaches checkout and revocation, and nothing else from
	// secrets or vars (Apple material, transport identity) is referenced.
	secretRef := regexp.MustCompile(`\b(secrets|vars)\.[A-Za-z0-9_]+`)
	for index, step := range test.Steps {
		text := stepText(t, &test.Steps[index])
		refs := secretRef.FindAllString(text, -1)
		if step.Name == "Create repository-scoped GitHub App token" {
			sort.Strings(refs)
			if !reflect.DeepEqual(refs, []string{"secrets.APP_PRIVATE_KEY", "vars.APP_CLIENT_ID"}) {
				t.Errorf("token step references %v", refs)
			}
		} else if len(refs) != 0 {
			t.Errorf("test job step %q references %v", step.Name, refs)
		}
		if strings.Contains(text, "steps.source-token.outputs.token") &&
			step.Name != "Checkout exactly the authorized private snapshot" && index != revokeIndex {
			t.Errorf("test job step %q receives the private repository token", step.Name)
		}
		for _, forbidden := range []string{"GITHUB_STEP_SUMMARY", "actions/cache", "download-artifact", "APPLE_", "ASC_", "github.token"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("test job step %q contains %q", step.Name, forbidden)
			}
		}
		if step.Uses != "" {
			action, _, _ := strings.Cut(step.Uses, "@")
			switch action {
			case "actions/checkout", "actions/setup-go", "actions/create-github-app-token", "actions/upload-artifact":
			default:
				t.Errorf("test job uses unexpected action %s", step.Uses)
			}
		}
	}

	// The script runs only through the trusted runner, with inputs as env.
	run := test.Steps[runIndex]
	if run.ID != "secure-test" || !run.ContinueOnError || !reflect.DeepEqual(run.Env, map[string]string{
		"IOS_PATH":           "${{ inputs.ios_path }}",
		"TEST_SCRIPT":        "${{ inputs.test_script }}",
		"ARTIFACT_RECIPIENT": "${{ inputs.artifact_recipient }}",
	}) {
		t.Errorf("test step = %+v", run)
	}
	for _, required := range []string{
		`"$RUNNER_TEMP/builder-runner" execute-tests`, `--source "$GITHUB_WORKSPACE/source"`,
		`--script "$TEST_SCRIPT"`, `--log "$RUNNER_TEMP/private-output/test.log"`,
		`--report-dir "$RUNNER_TEMP/private-output/report"`, "--timeout 105m",
		`--recipient "$ARTIFACT_RECIPIENT"`, `--output "$RUNNER_TEMP/encrypted"`,
	} {
		if !strings.Contains(run.Run, required) {
			t.Errorf("test step missing %q", required)
		}
	}
	if strings.Count(run.Run, "\n") != 9 || strings.Contains(run.Run, "bash") {
		t.Errorf("test step must invoke only the trusted runner:\n%s", run.Run)
	}

	// Only the two ciphertext files are uploaded, like the build job's upload.
	upload := test.Steps[stepIndex(&test, "Upload ciphertext only")]
	buildUpload := build.Steps[stepIndex(&build, "Upload ciphertext only")]
	if upload.Uses != buildUpload.Uses || upload.If != "always()" || upload.ID != "upload" {
		t.Errorf("test upload step = %+v", upload)
	}
	wantWith := map[string]string{
		"name":                 "ios-builder-${{ inputs.build_id }}",
		"path":                 "${{ runner.temp }}/encrypted/test.log.age\n${{ runner.temp }}/encrypted/report.md.age\n",
		"if-no-files-found":    "error",
		"retention-days":       "1",
		"include-hidden-files": "false",
		"compression-level":    "0",
	}
	if !reflect.DeepEqual(upload.With, wantWith) {
		t.Errorf("test upload with = %#v, want %#v", upload.With, wantWith)
	}

	// The public log says only passed or failed.
	status := test.Steps[stepIndex(&test, "Report private test status")]
	if status.If != "always()" || !reflect.DeepEqual(status.Env, map[string]string{
		"TEST_OUTCOME":   "${{ steps.secure-test.outcome }}",
		"UPLOAD_OUTCOME": "${{ steps.upload.outcome }}",
	}) {
		t.Errorf("status step = %+v", status)
	}
	var echoes []string
	for _, line := range strings.Split(status.Run, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "echo ") {
			echoes = append(echoes, trimmed)
		}
	}
	if !reflect.DeepEqual(echoes, []string{
		`echo "Encrypted artifact upload failed. No private diagnostics were printed."`,
		`echo "Tests failed. Download the encrypted report using Builder CLI."`,
		`echo "Tests passed"`,
	}) {
		t.Errorf("status step prints %q", echoes)
	}
}
