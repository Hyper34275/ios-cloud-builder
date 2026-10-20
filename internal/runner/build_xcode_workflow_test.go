package runner_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const (
	buildRunsOn = "${{ inputs.build_xcode == '27.1' && 'xcode-27' || 'macos-15' }}"
	selectBuild = "Select Xcode for the build"
)

func TestCentralWorkflowBuildXcode(t *testing.T) {
	workflow := loadStaticWorkflow(t)
	inputs := workflow.On.WorkflowDispatch.Inputs
	input := inputs["build_xcode"]
	if input.Required || input.Type != "choice" || input.Default != "26.3" || !reflect.DeepEqual(input.Options, []string{"26.3", "27.1"}) {
		t.Errorf("build_xcode input = %+v, want an optional choice of exactly 26.3 and 27.1 defaulting to 26.3", input)
	}

	build, deploy := workflow.Jobs["build"], workflow.Jobs["sign-and-deploy"]
	if build.RunsOn != buildRunsOn {
		t.Errorf("build job runs-on %q, want the build_xcode allowlist %q", build.RunsOn, buildRunsOn)
	}
	// DEVELOPER_DIR comes from the Select Xcode step, never a job-level env that
	// would pin Xcode 26.3 on the xcode-27 image.
	if len(build.Env) != 0 {
		t.Errorf("build job env = %v, want none: Xcode is selected by a step", build.Env)
	}

	// The protected signing job stays on the stable image and Xcode 26.3: it
	// runs codesign, security and altool, none of which reads an SDK, and it is
	// the job that holds the Apple credentials.
	if deploy.RunsOn != "macos-15" || deploy.Env["DEVELOPER_DIR"] != "/Applications/Xcode_26.3.app/Contents/Developer" || len(deploy.Env) != 1 {
		t.Errorf("sign-and-deploy runs-on %q env %v, want macos-15 and Xcode 26.3 only", deploy.RunsOn, deploy.Env)
	}
	deployText := stepsText(t, deploy.Steps)
	for _, forbidden := range []string{"build_xcode", "BUILD_XCODE", "xcode-27"} {
		if strings.Contains(deployText, forbidden) {
			t.Errorf("protected deployment job mentions %q", forbidden)
		}
	}

	// Every validation step passes the choice to the trusted runner.
	for _, name := range []string{"build", "test", "windows-test"} {
		job := workflow.Jobs[name]
		validate := job.Steps[stepIndex(&job, "Validate all dispatch inputs before credential creation")]
		wantFlag := `--build-xcode "$BUILD_XCODE"`
		if name == "windows-test" {
			wantFlag = `"--build-xcode=$env:BUILD_XCODE"`
		}
		if validate.Env["BUILD_XCODE"] != "${{ inputs.build_xcode }}" || !strings.Contains(validate.Run, wantFlag) {
			t.Errorf("%s job does not validate build_xcode before credential creation", name)
		}
	}

	// The Xcode is chosen after the private token is revoked and before any
	// project code runs, from the validated choice through env and a fixed case.
	selectIndex := stepIndex(&build, selectBuild)
	runIndex := stepIndex(&build, "Build unsigned IPA and encrypt private outputs")
	revokeIndex := stepIndex(&build, "Revoke private repository token before project code")
	if selectIndex < 0 || selectIndex > runIndex || selectIndex < revokeIndex {
		t.Fatalf("Select Xcode step at %d, revoke at %d, build at %d", selectIndex, revokeIndex, runIndex)
	}
	selectXcode := build.Steps[selectIndex]
	if !reflect.DeepEqual(selectXcode.Env, map[string]string{"BUILD_XCODE": "${{ inputs.build_xcode }}"}) ||
		!strings.Contains(selectXcode.Run, "27.1)") ||
		!strings.Contains(selectXcode.Run, "xcode=/Applications/Xcode_27.1_Release_Candidate.app") ||
		!strings.Contains(selectXcode.Run, "xcode=/Applications/Xcode_26.3.app") ||
		!strings.Contains(selectXcode.Run, `>> "$GITHUB_ENV"`) || strings.Contains(selectXcode.Run, "source") {
		t.Errorf("Select Xcode step = %+v", selectXcode)
	}
}

func stepsText(t *testing.T, steps []staticStep) string {
	t.Helper()
	var text strings.Builder
	for index := range steps {
		text.WriteString(stepText(t, &steps[index]))
	}
	return text.String()
}

// fakeXcode writes <root>/<name>/Contents/Developer/usr/bin/xcodebuild, which
// reports the given version and build like `xcodebuild -version`.
func fakeXcode(t *testing.T, root, name, version, build string) {
	t.Helper()
	bin := filepath.Join(root, name, "Contents", "Developer", "usr", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\necho 'Xcode %s'\necho 'Build version %s'\n", version, build)
	if err := os.WriteFile(filepath.Join(bin, "xcodebuild"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// selectBuildXcode runs the workflow's own Select Xcode script against a fake
// /Applications and returns the DEVELOPER_DIR it exports, relative to the fake
// root, or the script's failure output.
func selectBuildXcode(t *testing.T, buildXcode string, setup func(root string)) (selected string, output string, err error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the step is a bash script for macOS runners")
	}
	bash, lookErr := exec.LookPath("bash")
	if lookErr != nil {
		t.Skip("bash is not available")
	}
	workflow := loadStaticWorkflow(t)
	build := workflow.Jobs["build"]
	script := build.Steps[stepIndex(&build, selectBuild)].Run

	root := t.TempDir()
	setup(root)
	githubEnv := filepath.Join(root, "github_env")
	if writeErr := os.WriteFile(githubEnv, nil, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	script = strings.ReplaceAll(script, "/Applications", root)

	cmd := exec.Command(bash, "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", script)
	cmd.Env = append(os.Environ(), "BUILD_XCODE="+buildXcode, "GITHUB_ENV="+githubEnv)
	combined, err := cmd.CombinedOutput()
	output = string(combined)
	written, readErr := os.ReadFile(githubEnv)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, line := range strings.Split(string(written), "\n") {
		if value, ok := strings.CutPrefix(line, "DEVELOPER_DIR="); ok {
			rel, relErr := filepath.Rel(root, value)
			if relErr != nil {
				t.Fatal(relErr)
			}
			selected = filepath.ToSlash(rel)
		}
	}
	return selected, output, err
}

func TestSelectBuildXcodeStep(t *testing.T) {
	const rc = "Xcode_27.1_Release_Candidate.app"
	tests := []struct {
		name       string
		buildXcode string
		setup      func(t *testing.T, root string)
		want       string // DEVELOPER_DIR relative to the fake /Applications; empty means the step must fail
		wantOutput string
	}{
		{
			name: "27.1 prefers the release candidate over the beta names", buildXcode: "27.1",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, rc, "27.1", "27A9275")
				fakeXcode(t, root, "Xcode_27.1.app", "27.1", "27A9269")
				fakeXcode(t, root, "Xcode_27.0.app", "27.0", "27A5000")
				fakeXcode(t, root, "Xcode_26.3.app", "26.3", "17C500")
			},
			want: rc + "/Contents/Developer",
		},
		{
			name: "27.1 without the release candidate takes a release named Xcode_27.1.app", buildXcode: "27.1",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, "Xcode_27.1.app", "27.1", "27A9300")
				fakeXcode(t, root, "Xcode_27.2.app", "27.2", "27B1000")
			},
			want: "Xcode_27.1.app/Contents/Developer",
		},
		{
			name: "27.1 refuses a directory that reports another version", buildXcode: "27.1",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, "Xcode_27.1.app", "27.0", "27A5000")
			},
			wantOutput: "only a beta (or nothing) was found",
		},
		{
			name: "27.1 refuses a beta by name", buildXcode: "27.1",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, "Xcode_27.1_beta.app", "27.1", "27A9269")
			},
			wantOutput: "only a beta (or nothing) was found",
		},
		{
			name: "27.1 refuses a link that resolves to a beta", buildXcode: "27.1",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, "Xcode_27.1_Beta_2.app", "27.1", "27A9269")
				if err := os.Symlink(filepath.Join(root, "Xcode_27.1_Beta_2.app"), filepath.Join(root, "Xcode_27.1.app")); err != nil {
					t.Fatal(err)
				}
			},
			wantOutput: "only a beta (or nothing) was found",
		},
		{
			name: "27.1 refuses a build number with the beta suffix", buildXcode: "27.1",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, "Xcode_27.1.app", "27.1", "27A9269c")
			},
			wantOutput: "only a beta (or nothing) was found",
		},
		{
			name: "27.1 with no Xcode 27.1 at all fails", buildXcode: "27.1",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, "Xcode_26.3.app", "26.3", "17C500")
			},
			wantOutput: "only a beta (or nothing) was found",
		},
		{
			name: "27.1 skips a beta and takes a later release candidate-named link", buildXcode: "27.1",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, "Xcode_27.1.0.app", "27.1", "27A9269c")
				fakeXcode(t, root, "Xcode_27.1_RC.app", "27.1", "27A9275")
			},
			want: "Xcode_27.1_RC.app/Contents/Developer",
		},
		{
			name: "26.3 is Xcode 26.3", buildXcode: "26.3",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, "Xcode_26.3.app", "26.3", "17C500")
				fakeXcode(t, root, rc, "27.1", "27A9275")
			},
			want: "Xcode_26.3.app/Contents/Developer",
		},
		{
			name: "an empty choice is Xcode 26.3", buildXcode: "",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, "Xcode_26.3.app", "26.3", "17C500")
			},
			want: "Xcode_26.3.app/Contents/Developer",
		},
		{
			name: "an unknown value falls back to 26.3 and is never executed", buildXcode: "27.1; touch pwned",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, "Xcode_26.3.app", "26.3", "17C500")
				fakeXcode(t, root, rc, "27.1", "27A9275")
			},
			want: "Xcode_26.3.app/Contents/Developer",
		},
		{
			name: "26.3 missing fails", buildXcode: "26.3",
			setup: func(t *testing.T, root string) {
				fakeXcode(t, root, rc, "27.1", "27A9275")
			},
			wantOutput: "No suitable Xcode found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, output, err := selectBuildXcode(t, test.buildXcode, func(root string) { test.setup(t, root) })
			if test.want == "" {
				if err == nil {
					t.Fatalf("step succeeded with DEVELOPER_DIR %q:\n%s", got, output)
				}
				if got != "" || !strings.Contains(output, test.wantOutput) {
					t.Fatalf("DEVELOPER_DIR %q, output %q; want no selection and %q", got, output, test.wantOutput)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("DEVELOPER_DIR %q, err %v, output:\n%s\nwant %q", got, err, output, test.want)
			}
		})
	}
}
