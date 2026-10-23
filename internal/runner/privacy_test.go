package runner_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/MobAI-App/ios-builder/internal/runner"
)

func TestMaskCommandsCoverPrivateInputs(t *testing.T) {
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"inputs":{
		"build_id":"6f1c2a4e-0000-4000-8000-000000000000",
		"source_owner":"example-owner","source_repo":"secret-app",
		"ios_path":"SecretiOS","scheme":"Secret","test_script":"scripts/run-e2e.sh",
		"artifact_path":"dist/SecretSetup.exe","operation":"build"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	commands, err := runner.MaskCommands(event)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(commands)
	want := []string{
		"::add-mask::Secret", "::add-mask::SecretSetup", "::add-mask::SecretSetup.exe", "::add-mask::SecretiOS",
		"::add-mask::dist/SecretSetup.exe", "::add-mask::example-owner", "::add-mask::run-e2e",
		"::add-mask::run-e2e.sh", "::add-mask::scripts/run-e2e.sh", "::add-mask::secret-app",
	}
	if !reflect.DeepEqual(commands, want) {
		t.Errorf("mask commands = %q, want %q", commands, want)
	}
}

func TestMaskCommandsSkipGenericValues(t *testing.T) {
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"inputs":{"ios_path":".","scheme":"Runner","source_repo":"ab"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	commands, err := runner.MaskCommands(event)
	if err != nil || len(commands) != 0 {
		t.Errorf("mask commands = %q, %v; want none", commands, err)
	}
	if _, err := runner.MaskCommands(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing event file must fail, not leave inputs unmasked")
	}
}

func TestSealedBuildNumberRoundTrip(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := runner.SealBuildNumber("452", identity.Recipient().String())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "452") && len(sealed) < 10 {
		t.Fatalf("sealed value looks like plaintext: %q", sealed)
	}
	number, err := runner.UnsealBuildNumber(sealed, "2", identity.String())
	if err != nil || number != "452.2" {
		t.Fatalf("unsealed %q, %v; want 452.2", number, err)
	}
	other, _ := age.GenerateX25519Identity()
	if _, err := runner.UnsealBuildNumber(sealed, "1", other.String()); err == nil {
		t.Error("a different identity must not unseal the build number")
	}
	for _, bad := range []string{"", "0", "12a", "-1"} {
		if _, err := runner.SealBuildNumber(bad, identity.Recipient().String()); err == nil {
			t.Errorf("SealBuildNumber(%q) succeeded", bad)
		}
	}
	if _, err := runner.UnsealBuildNumber(sealed, "0", identity.String()); err == nil {
		t.Error("attempt 0 must be rejected")
	}
}

// privateInputRef matches an expression that hands a private dispatch input
// to a step. GitHub prints step env and with values in the public log, so
// every such step must come after the masking step.
var privateInputRef = regexp.MustCompile(`inputs\.(source_owner|source_repo|ios_path|scheme|test_script|artifact_path)\b`)

func TestCentralWorkflowMasksPrivateInputsBeforeUse(t *testing.T) {
	workflow := loadStaticWorkflow(t)
	for jobName, job := range workflow.Jobs {
		maskIndex := stepIndex(&job, "Mask private dispatch inputs")
		for index := range job.Steps {
			step := &job.Steps[index]
			if !privateInputRef.MatchString(stepText(t, step)) {
				continue
			}
			if maskIndex < 0 || index < maskIndex {
				t.Errorf("%s / %s receives a private input before it is masked", jobName, step.Name)
			}
		}
		if maskIndex < 0 {
			continue
		}
		mask := job.Steps[maskIndex]
		if len(mask.Env) != 0 || len(mask.With) != 0 || mask.Uses != "" || strings.Contains(mask.Run, "${{") ||
			!strings.Contains(mask.Run, "mask-inputs") || mask.If != "" {
			t.Errorf("%s mask step must only run the trusted runner, unconditionally: %+v", jobName, mask)
		}
	}
}

func TestCentralWorkflowPrintsNoPlainVariablesOrCounts(t *testing.T) {
	workflow := loadStaticWorkflow(t)
	// vars.* are never masked. Only values that are public by design may be
	// variables: the App client ID and the signing transport recipient.
	allowedVars := map[string]bool{"vars.APP_CLIENT_ID": true, "vars.APPLE_SIGNING_RECIPIENT": true}
	varRef := regexp.MustCompile(`\bvars\.[A-Za-z0-9_]+`)
	for jobName, job := range workflow.Jobs {
		for index := range job.Steps {
			step := &job.Steps[index]
			text := stepText(t, step)
			for _, ref := range varRef.FindAllString(text, -1) {
				if !allowedVars[ref] {
					t.Errorf("%s / %s prints %s unmasked; store it as a secret", jobName, step.Name, ref)
				}
			}
			for _, line := range strings.Split(step.Run, "\n") {
				if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "echo ") && strings.Contains(trimmed, "$COUNT") {
					t.Errorf("%s / %s echoes the private commit count: %s", jobName, step.Name, trimmed)
				}
			}
		}
		for name, value := range job.Outputs {
			if name != "sealed_build_number" || !strings.Contains(value, "outputs.sealed_build_number") {
				t.Errorf("%s output %s=%s is not sealed", jobName, name, value)
			}
		}
	}
}
