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
		"runs-on: windows-2025", "artifact_path:", "windows-test", "encrypted/artifact.age", "shell: pwsh",
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
	Defaults       staticDefaults    `yaml:"defaults"`
	Outputs        map[string]string `yaml:"outputs"`
	Env            map[string]string `yaml:"env"`
	Steps          []staticStep      `yaml:"steps"`
}

type staticDefaults struct {
	Run struct {
		Shell string `yaml:"shell"`
	} `yaml:"run"`
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
	if got := inputs["operation"].Options; !reflect.DeepEqual(got, []string{"build", "testflight", "test", "windows-test"}) {
		t.Errorf("operation options = %v", got)
	}
	for _, name := range []string{"test_script", "artifact_path"} {
		if input := inputs[name]; input.Required || input.Default != "" || input.Type != "string" {
			t.Errorf("%s input = %+v, want an optional string defaulting to empty", name, input)
		}
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("workflow permissions = %v", workflow.Permissions)
	}
	jobNames := make([]string, 0, len(workflow.Jobs))
	for name := range workflow.Jobs {
		jobNames = append(jobNames, name)
	}
	sort.Strings(jobNames)
	if !reflect.DeepEqual(jobNames, []string{"build", "sign-and-deploy", "test", "windows-test"}) {
		t.Fatalf("jobs = %v", jobNames)
	}
	build, deploy, test := workflow.Jobs["build"], workflow.Jobs["sign-and-deploy"], workflow.Jobs["test"]

	// Exactly one path runs per operation, and the test paths never reach the
	// protected Environment or its signing job.
	if build.If != "inputs.operation == 'build' || inputs.operation == 'testflight'" || test.If != "inputs.operation == 'test'" ||
		deploy.If != "inputs.operation == 'testflight'" || deploy.Needs != "build" ||
		workflow.Jobs["windows-test"].If != "inputs.operation == 'windows-test'" {
		t.Errorf("job conditions: build %q, test %q, deploy %q needs %q, windows-test %q",
			build.If, test.If, deploy.If, deploy.Needs, workflow.Jobs["windows-test"].If)
	}
	if !strings.Contains(workflow.RunName, "inputs.operation == 'windows-test' && 'Windows Test' || 'iOS Build'") ||
		!strings.HasSuffix(workflow.RunName, "${{ inputs.build_id }}") {
		t.Errorf("run-name = %q, want the operation's fixed title and the build ID", workflow.RunName)
	}
	if test.RunsOn != "macos-26" || test.TimeoutMinutes != 150 {
		t.Errorf("test job runs-on %q with timeout %d, want macos-26 and 150", test.RunsOn, test.TimeoutMinutes)
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
	if validate.Env["ARTIFACT_PATH"] != "${{ inputs.artifact_path }}" || !strings.Contains(validate.Run, `--artifact-path "$ARTIFACT_PATH"`) {
		t.Error("artifact_path is not validated (and so rejected) before credential creation")
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
		`--report-dir "$RUNNER_TEMP/private-output/report"`, "--timeout 135m",
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

// windowsRunnerLine matches one argument line of a PowerShell call to the
// trusted runner: a single double-quoted --name=value argument whose value is
// one environment variable or fixed text, with the line continuation.
var windowsRunnerLine = regexp.MustCompile(`^"--[a-z-]+=(?:\$env:[A-Z_]+)?[A-Za-z0-9\\._-]*"(?: ` + "`" + `)?$`)

func TestCentralWorkflowWindowsTestOperation(t *testing.T) {
	workflow := loadStaticWorkflow(t)
	job, iosTest, build := workflow.Jobs["windows-test"], workflow.Jobs["test"], workflow.Jobs["build"]
	if job.If != "inputs.operation == 'windows-test'" || job.RunsOn != "windows-2025" || job.TimeoutMinutes != 150 {
		t.Errorf("windows-test job: if %q, runs-on %q, timeout %d", job.If, job.RunsOn, job.TimeoutMinutes)
	}
	if job.Environment != "" || job.Needs != "" || len(job.Outputs) != 0 || len(job.Env) != 0 {
		t.Errorf("windows-test job must have no Environment, dependency, outputs, or job env: %+v", job)
	}
	if job.Defaults.Run.Shell != "pwsh" {
		t.Errorf("windows-test job shell = %q, want pwsh", job.Defaults.Run.Shell)
	}

	wantSteps := []string{
		"Checkout trusted public builder",
		"Set up trusted Go toolchain",
		"Build trusted runner before private checkout",
		"Validate all dispatch inputs before credential creation",
		"Create repository-scoped GitHub App token",
		"Checkout exactly the authorized private snapshot",
		"Revoke private repository token before project code",
		"Verify checkout credential cleanup",
		"Run project tests and encrypt private outputs",
		"Upload ciphertext only",
		"Report private test status",
	}
	gotSteps := make([]string, 0, len(job.Steps))
	for _, step := range job.Steps {
		gotSteps = append(gotSteps, step.Name)
	}
	if !reflect.DeepEqual(gotSteps, wantSteps) {
		t.Fatalf("windows-test steps = %q, want %q", gotSteps, wantSteps)
	}
	// The action steps (both checkouts, Go, and token minting) are the macOS
	// test job's, byte for byte; only the shell steps are PowerShell.
	for _, name := range []string{
		"Checkout trusted public builder", "Set up trusted Go toolchain",
		"Create repository-scoped GitHub App token", "Checkout exactly the authorized private snapshot",
	} {
		if !reflect.DeepEqual(job.Steps[stepIndex(&job, name)], iosTest.Steps[stepIndex(&iosTest, name)]) {
			t.Errorf("windows-test step %q differs from the macOS test job's", name)
		}
	}

	// The runner is built from the trusted checkout, after go mod verify.
	runnerBuild := job.Steps[stepIndex(&job, "Build trusted runner before private checkout")]
	if runnerBuild.WorkingDirectory != "builder" || !strings.HasPrefix(runnerBuild.Run, "go mod verify\nif ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }\n") ||
		!strings.Contains(runnerBuild.Run, `go build -trimpath -o "$env:RUNNER_TEMP\builder-runner.exe" ./cmd/builder-runner`) {
		t.Errorf("runner build step = %+v", runnerBuild)
	}

	// Every input is validated before any credential exists, with the same
	// environment as the build job's validation.
	validate := job.Steps[stepIndex(&job, "Validate all dispatch inputs before credential creation")]
	buildValidate := build.Steps[stepIndex(&build, "Validate all dispatch inputs before credential creation")]
	if !reflect.DeepEqual(validate.Env, buildValidate.Env) {
		t.Errorf("windows-test validation env %v differs from the build job's %v", validate.Env, buildValidate.Env)
	}
	for name := range validate.Env {
		flag := "--" + strings.ReplaceAll(strings.ToLower(name), "_", "-")
		if name == "ARTIFACT_RECIPIENT" {
			flag = "--artifact-recipient"
		}
		if want := `"` + flag + `=$env:` + name + `"`; strings.Count(validate.Run, want) != 1 {
			t.Errorf("validation does not pass %s exactly once as %s", name, want)
		}
	}

	revokeIndex := stepIndex(&job, "Revoke private repository token before project code")
	runIndex := stepIndex(&job, "Run project tests and encrypt private outputs")
	if revoke := job.Steps[revokeIndex]; revokeIndex > runIndex || revoke.If != "always() && steps.source-token.outcome == 'success'" ||
		!reflect.DeepEqual(revoke.Env, map[string]string{"SOURCE_TOKEN": "${{ steps.source-token.outputs.token }}"}) ||
		revoke.Run != "& \"$env:RUNNER_TEMP\\builder-runner.exe\" revoke-token\nexit $LASTEXITCODE\n" {
		t.Errorf("the private repository token is not revoked before the test script runs: %+v", job.Steps[revokeIndex])
	}
	verify := job.Steps[stepIndex(&job, "Verify checkout credential cleanup")]
	if verify.Run != "& \"$env:RUNNER_TEMP\\builder-runner.exe\" verify-checkout \"--source=$env:GITHUB_WORKSPACE\\source\"\nexit $LASTEXITCODE\n" {
		t.Errorf("checkout verification step = %q", verify.Run)
	}

	// Credentials: only the App key and client ID, only to mint the token,
	// which reaches only the checkout and its revocation.
	secretRef := regexp.MustCompile(`\b(secrets|vars)\.[A-Za-z0-9_]+`)
	for index, step := range job.Steps {
		text := stepText(t, &job.Steps[index])
		refs := secretRef.FindAllString(text, -1)
		if step.Name == "Create repository-scoped GitHub App token" {
			sort.Strings(refs)
			if !reflect.DeepEqual(refs, []string{"secrets.APP_PRIVATE_KEY", "vars.APP_CLIENT_ID"}) {
				t.Errorf("token step references %v", refs)
			}
		} else if len(refs) != 0 {
			t.Errorf("windows-test step %q references %v", step.Name, refs)
		}
		if strings.Contains(text, "steps.source-token.outputs.token") &&
			step.Name != "Checkout exactly the authorized private snapshot" && index != revokeIndex {
			t.Errorf("windows-test step %q receives the private repository token", step.Name)
		}
		for _, forbidden := range []string{
			"GITHUB_STEP_SUMMARY", "GITHUB_OUTPUT", "GITHUB_ENV", "actions/cache", "download-artifact", "APPLE_", "ASC_",
			"github.token", "Invoke-Expression", "iex ", "Start-Process", "Get-ChildItem", "env:*", "bash", "cmd /c", "-Command",
		} {
			if strings.Contains(text, forbidden) {
				t.Errorf("windows-test step %q contains %q", step.Name, forbidden)
			}
		}
		if step.Uses != "" {
			action, _, _ := strings.Cut(step.Uses, "@")
			switch action {
			case "actions/checkout", "actions/setup-go", "actions/create-github-app-token", "actions/upload-artifact":
			default:
				t.Errorf("windows-test job uses unexpected action %s", step.Uses)
			}
			continue
		}
		// staticStep has no shell field, so strict decoding already proves no
		// step overrides the job's pwsh default. A PowerShell step exits with the runner's own status, and every
		// runner argument is one quoted --name=value.
		if step.Name != "Report private test status" && !strings.HasSuffix(step.Run, "exit $LASTEXITCODE\n") {
			t.Errorf("windows-test step %q does not end with exit $LASTEXITCODE", step.Name)
		}
		if invocation, ok := strings.CutPrefix(step.Run, `& "$env:RUNNER_TEMP\builder-runner.exe" `); ok {
			lines := strings.Split(strings.TrimSuffix(invocation, "\nexit $LASTEXITCODE\n"), "\n")
			subcommand, rest, _ := strings.Cut(strings.TrimSuffix(lines[0], " `"), " ")
			if !regexp.MustCompile(`^[a-z-]+$`).MatchString(subcommand) {
				t.Errorf("windows-test step %q runs subcommand %q", step.Name, subcommand)
			}
			arguments := lines[1:]
			if rest != "" {
				arguments = append([]string{rest}, arguments...)
			}
			for _, argument := range arguments {
				if argument = strings.TrimSpace(argument); !windowsRunnerLine.MatchString(argument) {
					t.Errorf("windows-test step %q passes an unquoted or compound argument: %s", step.Name, argument)
				}
			}
		}
	}

	// The script runs only through the trusted runner, with inputs as env.
	run := job.Steps[runIndex]
	if run.ID != "secure-test" || !run.ContinueOnError || !reflect.DeepEqual(run.Env, map[string]string{
		"TEST_SCRIPT":        "${{ inputs.test_script }}",
		"ARTIFACT_PATH":      "${{ inputs.artifact_path }}",
		"ARTIFACT_RECIPIENT": "${{ inputs.artifact_recipient }}",
	}) {
		t.Errorf("windows test step = %+v", run)
	}
	wantRun := strings.Join([]string{
		"& \"$env:RUNNER_TEMP\\builder-runner.exe\" execute-tests `",
		"  \"--source=$env:GITHUB_WORKSPACE\\source\" `",
		"  \"--script=$env:TEST_SCRIPT\" `",
		"  \"--artifact=$env:ARTIFACT_PATH\" `",
		"  \"--log=$env:RUNNER_TEMP\\private-output\\test.log\" `",
		"  \"--report-dir=$env:RUNNER_TEMP\\private-output\\report\" `",
		"  \"--timeout=135m\" `",
		"  \"--recipient=$env:ARTIFACT_RECIPIENT\" `",
		"  \"--output=$env:RUNNER_TEMP\\encrypted\"",
		"exit $LASTEXITCODE",
		"",
	}, "\n")
	if run.Run != wantRun {
		t.Errorf("windows test step runs\n%s\nwant\n%s", run.Run, wantRun)
	}

	// Only the three ciphertext files are uploaded, like the other jobs.
	upload := job.Steps[stepIndex(&job, "Upload ciphertext only")]
	iosUpload := iosTest.Steps[stepIndex(&iosTest, "Upload ciphertext only")]
	if upload.Uses != iosUpload.Uses || upload.If != "always()" || upload.ID != "upload" {
		t.Errorf("windows upload step = %+v", upload)
	}
	wantWith := map[string]string{
		"name":                 "ios-builder-${{ inputs.build_id }}",
		"path":                 "${{ runner.temp }}/encrypted/test.log.age\n${{ runner.temp }}/encrypted/report.md.age\n${{ runner.temp }}/encrypted/artifact.age\n",
		"if-no-files-found":    "error",
		"retention-days":       "1",
		"include-hidden-files": "false",
		"compression-level":    "0",
	}
	if !reflect.DeepEqual(upload.With, wantWith) {
		t.Errorf("windows upload with = %#v, want %#v", upload.With, wantWith)
	}

	// The public log says only passed or failed.
	status := job.Steps[stepIndex(&job, "Report private test status")]
	if status.If != "always()" || !reflect.DeepEqual(status.Env, map[string]string{
		"TEST_OUTCOME":   "${{ steps.secure-test.outcome }}",
		"UPLOAD_OUTCOME": "${{ steps.upload.outcome }}",
	}) {
		t.Errorf("windows status step = %+v", status)
	}
	var writes []string
	for _, line := range strings.Split(status.Run, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "Write-") || strings.Contains(trimmed, "echo") {
			writes = append(writes, trimmed)
		}
	}
	if !reflect.DeepEqual(writes, []string{
		"Write-Output 'Encrypted artifact upload failed. No private diagnostics were printed.'",
		"Write-Output 'Tests failed. Download the encrypted report using Builder CLI.'",
		"Write-Output 'Tests passed'",
	}) {
		t.Errorf("windows status step prints %q", writes)
	}
}
