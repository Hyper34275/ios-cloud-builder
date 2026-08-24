// Package runner implements the small trusted helper used by the public
// central-builder workflow. It deliberately accepts structured iOS build
// options rather than commands or scripts.
package runner

import (
	"fmt"
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
)

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
}

// Validate rejects values that could widen repository access, escape the
// source checkout, or turn the workflow into a generic command runner.
func (in Inputs) Validate() error {
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
