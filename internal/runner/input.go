// Package runner implements the small trusted helper used by the public
// central-builder workflow. It deliberately accepts structured iOS build
// options rather than commands. The one script it runs is a test operation's
// test_script: a validated path to a file inside the authorized snapshot, never
// a command line.
package runner

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"filippo.io/age"
)

const (
	FrameworkAuto        = "auto"
	FrameworkNative      = "native"
	FrameworkFlutter     = "flutter"
	FrameworkReactNative = "react-native"
	FrameworkExpo        = "expo"
	FrameworkKMP         = "kmp"
	FrameworkCordova     = "cordova"
	FrameworkIonic       = "ionic"
)

var (
	buildIDPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	repoPartPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,98}[A-Za-z0-9])?$`)
	schemePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+()-]{0,127}$`)
	// A test script path segment: portable file-name characters only, and no
	// leading '-' so no segment can be read as a bash option.
	testScriptSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9._+][A-Za-z0-9._+-]*$`)
)

// MaxTestScriptPathLength bounds the test_script input.
const MaxTestScriptPathLength = 256

// Inputs are the complete set of values accepted from workflow_dispatch.
type Inputs struct {
	BuildID           string
	SourceOwner       string
	SourceRepo        string
	SnapshotRef       string
	IOSPath           string
	Scheme            string
	Configuration     string
	FrameworkHint     string
	ArtifactRecipient string
	Operation         string
	TestScript        string
}

// Validate rejects values that could widen repository access, escape the
// source checkout, or turn the workflow into a generic command runner.
func (in *Inputs) Validate() error {
	if !buildIDPattern.MatchString(in.BuildID) || in.BuildID == "." || in.BuildID == ".." {
		return fmt.Errorf("invalid build_id")
	}
	if !repoPartPattern.MatchString(in.SourceOwner) {
		return fmt.Errorf("invalid source_owner")
	}
	if !repoPartPattern.MatchString(in.SourceRepo) || strings.HasSuffix(in.SourceRepo, ".git") {
		return fmt.Errorf("invalid source_repo")
	}
	wantRef := "refs/ios-builder/jobs/" + in.BuildID
	if in.SnapshotRef != wantRef {
		return fmt.Errorf("snapshot_ref must be exactly %q", wantRef)
	}
	if err := validateRelativePath(in.IOSPath); err != nil {
		return fmt.Errorf("invalid ios_path: %w", err)
	}
	if in.Scheme != "" && !schemePattern.MatchString(in.Scheme) {
		return fmt.Errorf("invalid scheme")
	}
	if in.Configuration != "Debug" && in.Configuration != "Release" {
		return fmt.Errorf("configuration must be Debug or Release")
	}
	if !validFramework(in.FrameworkHint) {
		return fmt.Errorf("invalid framework_hint")
	}
	recipient, err := age.ParseX25519Recipient(in.ArtifactRecipient)
	if err != nil || recipient.String() != in.ArtifactRecipient {
		return fmt.Errorf("invalid artifact_recipient")
	}
	switch in.Operation {
	case "build", "testflight":
		if in.TestScript != "" {
			return fmt.Errorf("test_script is accepted only for operation test")
		}
	case "test":
		if err := ValidateTestScriptPath(in.TestScript); err != nil {
			return fmt.Errorf("invalid test_script: %w", err)
		}
	default:
		return fmt.Errorf("invalid operation")
	}
	return nil
}

// ValidateTestScriptPath checks a test_script value before the private
// checkout exists: a clean, forward-slash path relative to the checkout root,
// with no traversal, Git metadata, control characters, or unusual characters.
// ResolveTestScript later proves that it names a regular file in the checkout.
// Error messages never echo the value.
func ValidateTestScriptPath(value string) error {
	switch {
	case value == "":
		return fmt.Errorf("is required")
	case len(value) > MaxTestScriptPathLength:
		return fmt.Errorf("is longer than %d bytes", MaxTestScriptPathLength)
	case strings.HasPrefix(value, "/"):
		return fmt.Errorf("must be relative to the repository root")
	case strings.Contains(value, `\`):
		return fmt.Errorf("must use forward slashes")
	case strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return fmt.Errorf("must not contain control characters")
	}
	for _, segment := range strings.Split(value, "/") {
		switch {
		case segment == "..":
			return fmt.Errorf("must remain inside the repository")
		case strings.EqualFold(segment, ".git"): // APFS is case-insensitive by default
			return fmt.Errorf("must not point into Git metadata")
		case segment == "" || segment == ".":
			return fmt.Errorf("must be a clean path")
		case !testScriptSegmentPattern.MatchString(segment):
			return fmt.Errorf("may contain only letters, digits, '.', '_', '+', '-' and '/', and no segment may start with '-'")
		}
	}
	if path.Clean(value) != value {
		return fmt.Errorf("must be a clean path")
	}
	return nil
}

func validateRelativePath(value string) error {
	if value == "" || filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") || strings.Contains(value, `\`) {
		return fmt.Errorf("must be a non-empty portable relative path")
	}
	clean := filepath.Clean(value)
	if clean != value || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("must be clean and remain inside the checkout")
	}
	return nil
}

func validFramework(framework string) bool {
	switch framework {
	case FrameworkAuto, FrameworkNative, FrameworkFlutter, FrameworkReactNative,
		FrameworkExpo, FrameworkKMP, FrameworkCordova, FrameworkIonic:
		return true
	default:
		return false
	}
}
