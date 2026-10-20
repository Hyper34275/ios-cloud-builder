package runner

import (
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
)

func TestInputsValidateBuildXcode(t *testing.T) {
	for _, operation := range []string{"build", "testflight"} {
		in := validInputs(t)
		in.Operation = operation
		for _, xcode := range []string{"", "26.3", "27.1"} {
			in.BuildXcode = xcode
			if err := in.Validate(); err != nil {
				t.Errorf("operation %s with build_xcode %q rejected: %v", operation, xcode, err)
			}
		}
		for _, xcode := range []string{"27.0", "27.2", "26", "26.4", "27", "27.1.0", "27.1_Release_Candidate", "xcode-27", "macos-15", " 27.1", "27.1\n", "27.1; id", "${{ secrets.X }}"} {
			in.BuildXcode = xcode
			if err := in.Validate(); err == nil {
				t.Errorf("operation %s accepted build_xcode %q", operation, xcode)
			}
		}
	}
}

// 27.1 selects the build job's runner, so no other operation may carry it; the
// default is accepted everywhere because the workflow always sends the default
// input value to the validation step.
func TestInputsValidateBuildXcodeOnOtherOperations(t *testing.T) {
	for _, operation := range []string{"test", "windows-test"} {
		in := validInputs(t)
		in.Operation = operation
		in.TestScript = "scripts/test.sh"
		if operation == "windows-test" {
			in.TestScript = "scripts/test.ps1"
		}
		for _, xcode := range []string{"", "26.3"} {
			in.BuildXcode = xcode
			if err := in.Validate(); err != nil {
				t.Errorf("operation %s with build_xcode %q rejected: %v", operation, xcode, err)
			}
		}
		in.BuildXcode = "27.1"
		if err := in.Validate(); err == nil {
			t.Errorf("operation %s accepted build_xcode 27.1", operation)
		}
	}
}

func TestBuildXcodeValidationMatchesConfig(t *testing.T) {
	if BuildXcode263 != config.BuildXcode263 || BuildXcode271 != config.BuildXcode271 {
		t.Fatal("the runner and config build Xcode constants differ")
	}
	for _, xcode := range []string{"26.3", "27.1", "27.0", "27.2", "26", "", "x", "27.1 "} {
		in := validInputs(t)
		in.Operation = "testflight"
		in.BuildXcode = xcode
		runnerErr := in.Validate()
		configErr := config.ValidateBuildXcode(xcode)
		if xcode == "" {
			configErr = nil // the empty Xcode is the default on both sides
		}
		if (runnerErr == nil) != (configErr == nil) {
			t.Errorf("%q: runner error %v, config error %v; the CLI and workflow must agree", xcode, runnerErr, configErr)
		}
	}
}
