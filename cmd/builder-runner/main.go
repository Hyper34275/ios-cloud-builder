// builder-runner is the trusted executable built from the public builder
// checkout before private source or credentials enter the job.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/MobAI-App/ios-builder/internal/runner"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("builder-runner requires a subcommand")
	}
	switch args[0] {
	case "validate-inputs":
		return validateInputs(args[1:])
	case "revoke-token":
		return revokeToken()
	case "detect":
		return detect(args[1:])
	case "verify-checkout":
		return verifyCheckout(args[1:])
	case "execute":
		return execute(args[1:])
	case "execute-macos":
		return executeMacOS(args[1:])
	case "execute-tests":
		return executeTests(args[1:])
	case "deploy-testflight":
		return deployTestFlight(args[1:])
	case "mask-inputs":
		return maskInputs()
	case "seal-build-number":
		return sealBuildNumber(args[1:])
	default:
		return fmt.Errorf("unknown builder-runner subcommand")
	}
}

func deployTestFlight(args []string) error {
	flags := newFlags("deploy-testflight")
	var options runner.TestFlightOptions
	var recipient, outputDir, sealedBuildNumber, buildAttempt string
	flags.StringVar(&options.EncryptedIPAPath, "encrypted-ipa", "", "")
	flags.StringVar(&options.LogPath, "log", "", "")
	flags.StringVar(&options.BuildNumber, "build-number", "", "")
	flags.StringVar(&sealedBuildNumber, "sealed-build-number-file", "", "")
	flags.StringVar(&buildAttempt, "build-attempt", "", "")
	flags.StringVar(&options.Platform, "platform", runner.PlatformIOS, "")
	flags.StringVar(&recipient, "recipient", "", "")
	flags.StringVar(&outputDir, "output", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("invalid TestFlight deployment arguments")
	}
	if sealedBuildNumber != "" {
		number, err := runner.UnsealBuildNumber(sealedBuildNumber, buildAttempt, os.Getenv("APPLE_SIGNING_AGE_IDENTITY"))
		if err != nil {
			return fmt.Errorf("the sealed TestFlight build number is missing or invalid")
		}
		options.BuildNumber = number
	}
	if options.Platform == runner.PlatformMacOS {
		fmt.Println("Signing and uploading macOS application; detailed output is private")
	} else {
		fmt.Println("Signing and uploading iOS application; detailed output is private")
	}
	if err := runner.ExecuteTestFlight(context.Background(), &options, recipient, outputDir); err != nil {
		if err == runner.ErrDeployFailed {
			return runner.ErrDeployFailed
		}
		return fmt.Errorf("secure TestFlight deployment preparation failed")
	}
	fmt.Println("App Store Connect accepted the upload; encrypted diagnostics are ready")
	return nil
}

// maskInputs registers the private dispatch inputs as log masks before any
// step receives them. It prints only workflow commands, which the runner
// consumes instead of writing to the public log.
func maskInputs() error {
	commands, err := runner.MaskCommands(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return fmt.Errorf("private inputs could not be masked")
	}
	for _, command := range commands {
		fmt.Println(command)
	}
	fmt.Println("Private dispatch inputs masked")
	return nil
}

// sealBuildNumber writes the BUILD_COUNT environment value, encrypted to the
// signing recipient, to the --output file. It prints only a fixed line.
func sealBuildNumber(args []string) error {
	flags := newFlags("seal-build-number")
	var recipient, output string
	flags.StringVar(&recipient, "recipient", "", "")
	flags.StringVar(&output, "output", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("invalid build number sealing arguments")
	}
	count := os.Getenv("BUILD_COUNT")
	_ = os.Unsetenv("BUILD_COUNT")
	if err := runner.SealBuildNumber(count, recipient, output); err != nil {
		return fmt.Errorf("build number could not be sealed")
	}
	fmt.Println("TestFlight build number sealed for the signing job")
	return nil
}

func newFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	return flags
}

func validateInputs(args []string) error {
	flags := newFlags("validate-inputs")
	var in runner.Inputs
	flags.StringVar(&in.BuildID, "build-id", "", "")
	flags.StringVar(&in.SourceOwner, "source-owner", "", "")
	flags.StringVar(&in.SourceRepo, "source-repo", "", "")
	flags.StringVar(&in.SnapshotRef, "snapshot-ref", "", "")
	flags.StringVar(&in.IOSPath, "ios-path", "", "")
	flags.StringVar(&in.Scheme, "scheme", "", "")
	flags.StringVar(&in.Configuration, "configuration", "", "")
	flags.StringVar(&in.FrameworkHint, "framework-hint", "", "")
	flags.StringVar(&in.ArtifactRecipient, "artifact-recipient", "", "")
	flags.StringVar(&in.Operation, "operation", "", "")
	flags.StringVar(&in.TestScript, "test-script", "", "")
	flags.StringVar(&in.ArtifactPath, "artifact-path", "", "")
	flags.StringVar(&in.TestImage, "test-image", "", "")
	flags.StringVar(&in.BuildXcode, "build-xcode", "", "")
	flags.StringVar(&in.Platform, "platform", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("invalid validation arguments")
	}
	if err := in.Validate(); err != nil {
		return err
	}
	fmt.Println("Dispatch inputs validated")
	return nil
}

func revokeToken() error {
	token := os.Getenv("SOURCE_TOKEN")
	_ = os.Unsetenv("SOURCE_TOKEN")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := runner.RevokeInstallationToken(ctx, token); err != nil {
		return err
	}
	fmt.Println("Source credential revoked")
	return nil
}

func detect(args []string) error {
	flags := newFlags("detect")
	var source, iosPath, hint string
	flags.StringVar(&source, "source", "", "")
	flags.StringVar(&iosPath, "ios-path", "", "")
	flags.StringVar(&hint, "framework", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !filepath.IsAbs(source) {
		return fmt.Errorf("invalid framework detection arguments")
	}
	framework, err := runner.DetectFramework(source, iosPath, hint)
	if err != nil {
		return fmt.Errorf("framework detection failed")
	}
	fmt.Printf("framework=%s\n", framework)
	return nil
}

func verifyCheckout(args []string) error {
	flags := newFlags("verify-checkout")
	var source string
	flags.StringVar(&source, "source", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !filepath.IsAbs(source) {
		return fmt.Errorf("invalid checkout verification arguments")
	}
	if err := runner.VerifyCheckoutNoCredentials(source); err != nil {
		return err
	}
	fmt.Println("Private checkout contains no persisted credential")
	return nil
}

func execute(args []string) error {
	flags := newFlags("execute")
	var options runner.BuildOptions
	var recipient, ipaRecipient, outputDir string
	flags.StringVar(&options.SourceRoot, "source", "", "")
	flags.StringVar(&options.IOSPath, "ios-path", "", "")
	flags.StringVar(&options.Scheme, "scheme", "", "")
	flags.StringVar(&options.Configuration, "configuration", "", "")
	flags.StringVar(&options.Framework, "framework", "", "")
	flags.StringVar(&options.LogPath, "log", "", "")
	flags.StringVar(&options.IPAPath, "ipa", "", "")
	flags.StringVar(&recipient, "recipient", "", "")
	flags.StringVar(&ipaRecipient, "ipa-recipient", "", "")
	flags.StringVar(&outputDir, "output", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("invalid secure execution arguments")
	}
	if ipaRecipient == "" {
		ipaRecipient = recipient
	}
	fmt.Println("Building iOS application; detailed output is private")
	if err := runner.ExecuteSecureWithIPARecipient(context.Background(), &options, recipient, ipaRecipient, outputDir); err != nil {
		if err == runner.ErrBuildFailed {
			return runner.ErrBuildFailed
		}
		return fmt.Errorf("secure build artifact preparation failed")
	}
	fmt.Println("Build succeeded; encrypted artifacts are ready")
	return nil
}

// executeMacOS runs the project's macOS build script for a Mac App Store
// deployment and encrypts its private log for the caller and the application
// it built for the protected signing job. Only fixed lines reach the public
// job log.
func executeMacOS(args []string) error {
	flags := newFlags("execute-macos")
	var options runner.MacOSBuildOptions
	var recipient, ipaRecipient, outputDir string
	flags.StringVar(&options.SourceRoot, "source", "", "")
	flags.StringVar(&options.Script, "script", "", "")
	flags.StringVar(&options.App, "app", "", "")
	flags.StringVar(&options.LogPath, "log", "", "")
	flags.StringVar(&options.IPAPath, "ipa", "", "")
	flags.DurationVar(&options.Timeout, "timeout", 0, "")
	flags.StringVar(&recipient, "recipient", "", "")
	flags.StringVar(&ipaRecipient, "ipa-recipient", "", "")
	flags.StringVar(&outputDir, "output", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("invalid secure macOS build arguments")
	}
	if ipaRecipient == "" {
		ipaRecipient = recipient
	}
	fmt.Println("Building macOS application; detailed output is private")
	if err := runner.ExecuteMacOSBuildSecure(context.Background(), &options, recipient, ipaRecipient, outputDir); err != nil {
		if err == runner.ErrBuildFailed {
			return runner.ErrBuildFailed
		}
		return fmt.Errorf("secure build artifact preparation failed")
	}
	fmt.Println("Build succeeded; encrypted artifacts are ready")
	return nil
}

// executeTests runs the project's test script. Only the two fixed lines below
// and a fixed error reach the public job log; the script's own output goes to
// the private log, which is encrypted with the optional report and, after a
// pass, the optional artifact.
func executeTests(args []string) error {
	flags := newFlags("execute-tests")
	var options runner.TestOptions
	var recipient, outputDir string
	flags.StringVar(&options.SourceRoot, "source", "", "")
	flags.StringVar(&options.IOSPath, "ios-path", "", "")
	flags.StringVar(&options.Script, "script", "", "")
	flags.StringVar(&options.Artifact, "artifact", "", "")
	flags.StringVar(&options.LogPath, "log", "", "")
	flags.StringVar(&options.ReportDir, "report-dir", "", "")
	flags.DurationVar(&options.Timeout, "timeout", 0, "")
	flags.StringVar(&recipient, "recipient", "", "")
	flags.StringVar(&outputDir, "output", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("invalid secure test arguments")
	}
	fmt.Println("Running project tests; detailed output is private")
	if err := runner.ExecuteTestsSecure(context.Background(), &options, recipient, outputDir); err != nil {
		if errors.Is(err, runner.ErrTestsFailed) {
			return runner.ErrTestsFailed
		}
		return fmt.Errorf("secure test artifact preparation failed")
	}
	if options.Artifact != "" {
		fmt.Println("Tests passed; encrypted report and artifact are ready")
	} else {
		fmt.Println("Tests passed; encrypted report is ready")
	}
	return nil
}
