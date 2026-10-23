package config

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"filippo.io/age"
)

var (
	ownerPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	workflowPattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.ya?ml$`)
	schemePattern     = regexp.MustCompile(`^$|^[A-Za-z0-9][A-Za-z0-9 ._+()-]{0,127}$`)
	// testScriptSegmentPattern must match the central runner's rule exactly.
	testScriptSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9._+][A-Za-z0-9._+-]*$`)
)

// MaxTestScriptPathLength is the longest test script path the central
// workflow accepts.
const MaxTestScriptPathLength = 256

// MaxArtifactPathLength is the longest artifact path the central workflow
// accepts.
const MaxArtifactPathLength = MaxTestScriptPathLength

// Runner images `builder ios test` can run on. The central workflow's
// test_image input is a choice of exactly these two values and the job's
// runs-on is derived from it, so a value outside this list can never become a
// runner label.
const (
	// TestImageMacOS26 is the default: the macos-26 image with Xcode 26.3.
	TestImageMacOS26 = "macos-26"
	// TestImageXcode27 is the xcode-27 image: macOS 27 with the newest Xcode 27.x.
	TestImageXcode27 = "xcode-27"
)

// TestImages lists every accepted test image, the default first.
func TestImages() []string { return []string{TestImageMacOS26, TestImageXcode27} }

// ValidateTestImage accepts exactly the runner images in TestImages. The empty
// string means the default and is accepted by callers that allow it.
func ValidateTestImage(value string) error {
	for _, image := range TestImages() {
		if value == image {
			return nil
		}
	}
	return fmt.Errorf("must be one of %s", strings.Join(TestImages(), ", "))
}

// Xcode versions `builder ios build` and `builder ios deploy` can build with.
// The central workflow's build_xcode input is a choice of exactly these two
// values and the build job's runs-on is derived from it, so a value outside this
// list can never become a runner label.
const (
	// BuildXcode263 is the default: Xcode 26.3 on the macos-15 image.
	BuildXcode263 = "26.3"
	// BuildXcode271 is Xcode 27.1 (the release candidate, never a beta) on the
	// xcode-27 image, which builds with the iOS 27.1 SDK.
	BuildXcode271 = "27.1"
)

// BuildXcodes lists every accepted build Xcode, the default first.
func BuildXcodes() []string { return []string{BuildXcode263, BuildXcode271} }

// ValidateBuildXcode accepts exactly the versions in BuildXcodes. The empty
// string means the default and is accepted by callers that allow it.
func ValidateBuildXcode(value string) error {
	for _, version := range BuildXcodes() {
		if value == version {
			return nil
		}
	}
	return fmt.Errorf("must be one of %s", strings.Join(BuildXcodes(), ", "))
}

// Backend identifies where a build workflow is executed.
type Backend string

const (
	// BackendRepository preserves the original ios-builder behavior: the
	// workflow lives in the source repository itself.
	BackendRepository Backend = "repository"
	// BackendCentral dispatches the build to a separate public builder
	// repository. GitHub still identifies the private source repository.
	BackendCentral Backend = "central"

	// DefaultWorkflow is the workflow file used by the central builder unless
	// the project explicitly selects another one.
	DefaultWorkflow = "ios-build.yml"
)

// Config represents the builder.json configuration file
type Config struct {
	Project     string            `json:"project"`
	Platform    string            `json:"platform"`
	Backend     Backend           `json:"backend,omitempty"`
	GitHub      GitHubConfig      `json:"github"`
	Builder     BuilderConfig     `json:"builder,omitempty"`
	Security    SecurityConfig    `json:"security,omitempty"`
	IOS         IOSConfig         `json:"ios,omitempty"`
	Flutter     FlutterConfig     `json:"flutter,omitempty"`
	ReactNative ReactNativeConfig `json:"reactNative,omitempty"`
	KMP         KMPConfig         `json:"kmp,omitempty"`
	MobAI       MobAIConfig       `json:"mobai,omitempty"`
	// Windows is omitted when empty, so saving a configuration never adds a
	// section its project does not use.
	Windows WindowsConfig `json:"windows,omitzero"`
	// MacOS is omitted when empty, like Windows.
	MacOS MacOSConfig `json:"macos,omitzero"`
}

// MacOSConfig holds what `builder macos deploy` needs to deploy a macOS
// application to the Mac App Store through the central builder.
type MacOSConfig struct {
	// BuildScript builds the application on the central macOS runner,
	// relative to the repository root (e.g. "scripts/build-app-store.sh"). It
	// must sign the bundle ad hoc with the application's sandbox entitlements.
	BuildScript string `json:"buildScript,omitempty"`
	// App is the application bundle the script leaves behind, relative to the
	// repository root (e.g. "dist/App.app").
	App string `json:"app,omitempty"`
}

// WindowsConfig holds the defaults of `builder windows test`, which runs a
// test script on a Windows runner of the central builder.
type WindowsConfig struct {
	// TestScript is the .ps1 or .sh script to run, relative to the repository
	// root (e.g. "scripts/windows-test.ps1").
	TestScript string `json:"testScript,omitempty"`
	// Artifact is the file a passing script builds, relative to the repository
	// root (e.g. "dist/Setup.exe"). It is encrypted, downloaded and decrypted
	// to the output directory under its base name.
	Artifact string `json:"artifact,omitempty"`
}

// FlutterConfig holds Flutter-specific settings
type FlutterConfig struct {
	Version string      `json:"version,omitempty"` // Pinned Flutter version (e.g., "3.24.0")
	Watch   WatchConfig `json:"watch,omitempty"`   // File watcher settings for hot reload
}

// WatchConfig holds file watcher settings for hot reload
type WatchConfig struct {
	Dirs     []string `json:"dirs,omitempty"`     // Directories to watch
	Patterns []string `json:"patterns,omitempty"` // File patterns to match (by suffix)
	Ignore   []string `json:"ignore,omitempty"`   // Patterns to ignore (by suffix)
	Debounce int      `json:"debounce,omitempty"` // Debounce ms
}

// ReactNativeConfig holds React Native-specific settings
type ReactNativeConfig struct {
	MetroPort int  `json:"metroPort,omitempty"` // Metro bundler port (default: 8081)
	Expo      bool `json:"expo,omitempty"`      // Whether this is an Expo project
}

// KMPConfig holds Kotlin Multiplatform-specific settings.
// The iOS app is built with xcodebuild, which invokes Gradle (via the Xcode
// "Run Script" build phase, or CocoaPods) to compile the shared Kotlin
// framework. The CI build therefore needs a JDK available for Gradle.
type KMPConfig struct {
	JDKVersion string `json:"jdkVersion,omitempty"` // JDK version for Gradle builds (default: 17)
}

// IOSConfig holds iOS build settings
type IOSConfig struct {
	// Path to iOS project relative to repo root (e.g., "ios" for React Native, "platforms/ios" for Cordova)
	// Empty means root directory contains the Xcode project
	Path          string `json:"path,omitempty"`
	Scheme        string `json:"scheme,omitempty"`        // Xcode scheme to build (auto-detected if empty)
	Signing       bool   `json:"signing,omitempty"`       // Whether code signing is configured
	Configuration string `json:"configuration,omitempty"` // Build configuration: Debug (faster) or Release (production)
	// TestScript is the script `builder ios test` runs on the central builder,
	// relative to the repository root (e.g. "scripts/ios-test.sh").
	TestScript string `json:"testScript,omitempty"`
	// TestImage is the runner image `builder ios test` uses: macos-26 (the
	// default when empty) or xcode-27. `--image` overrides it.
	TestImage string `json:"testImage,omitempty"`
	// BuildXcode is the Xcode `builder ios build` and `builder ios deploy` use:
	// 26.3 (the default when empty) or 27.1. `--xcode` overrides it.
	BuildXcode string `json:"buildXcode,omitempty"`
}

// MobAIConfig holds MobAI settings for local development
type MobAIConfig struct {
	URL      string `json:"url,omitempty"`       // MobAI API URL (default: http://localhost:8686)
	DeviceID string `json:"device_id,omitempty"` // Preferred device ID (default: first available)
}

// GitHubConfig holds GitHub repository settings
type GitHubConfig struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// BuilderConfig identifies the public repository that owns the central build
// workflow. It is distinct from GitHubConfig, which always identifies the
// private source repository in central mode.
type BuilderConfig struct {
	Owner    string `json:"owner,omitempty"`
	Repo     string `json:"repo,omitempty"`
	Workflow string `json:"workflow,omitempty"`
}

// SecurityConfig contains public cryptographic configuration. Recipient is an
// AGE X25519 recipient; the corresponding identity is local-only and must
// never be written to builder.json.
type SecurityConfig struct {
	Recipient string `json:"recipient,omitempty"`
}

// ApplyDefaults migrates an in-memory configuration to the current schema.
// It returns true when a value changed. Configurations written by upstream
// versions did not have a backend, and therefore retain repository behavior.
func (c *Config) ApplyDefaults() bool {
	changed := false
	if c.Backend == "" {
		// No upstream release had builder/security fields, so their presence
		// unambiguously identifies a pre-backend central configuration.
		if strings.TrimSpace(c.Builder.Owner) != "" ||
			strings.TrimSpace(c.Builder.Repo) != "" ||
			strings.TrimSpace(c.Builder.Workflow) != "" ||
			strings.TrimSpace(c.Security.Recipient) != "" {
			c.Backend = BackendCentral
		} else {
			c.Backend = BackendRepository
		}
		changed = true
	}
	if c.Backend == BackendCentral && strings.TrimSpace(c.Builder.Workflow) == "" {
		c.Builder.Workflow = DefaultWorkflow
		changed = true
	}
	return changed
}

// Migrate is an alias for ApplyDefaults, provided for callers that explicitly
// migrate and persist an existing builder.json.
func (c *Config) Migrate() bool { return c.ApplyDefaults() }

// IsCentral reports whether the separate central-builder backend is selected.
func (c *Config) IsCentral() bool { return c.Backend == BackendCentral }

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Project) == "" || len(c.Project) > 128 || strings.ContainsAny(c.Project, "\r\n\x00") {
		return &ValidationError{Field: "project", Message: "project name is required"}
	}
	if !validOwner(c.GitHub.Owner) {
		return &ValidationError{Field: "github.owner", Message: "must be a valid GitHub owner"}
	}
	if !validRepository(c.GitHub.Repo) {
		return &ValidationError{Field: "github.repo", Message: "must be a valid GitHub repository name"}
	}
	if c.Backend != "" && c.Backend != BackendRepository && c.Backend != BackendCentral {
		return &ValidationError{Field: "backend", Message: "must be repository or central"}
	}
	if c.Backend == BackendCentral {
		if !validOwner(c.Builder.Owner) {
			return &ValidationError{Field: "builder.owner", Message: "must be a valid GitHub owner"}
		}
		if !validRepository(c.Builder.Repo) {
			return &ValidationError{Field: "builder.repo", Message: "must be a valid GitHub repository name"}
		}
		if len(c.Builder.Workflow) > 255 || !workflowPattern.MatchString(c.Builder.Workflow) {
			return &ValidationError{Field: "builder.workflow", Message: "must be a workflow YAML file name, not a path"}
		}
		if strings.TrimSpace(c.Security.Recipient) == "" {
			return &ValidationError{Field: "security.recipient", Message: "AGE recipient is required in central mode"}
		}
		if _, err := age.ParseX25519Recipient(strings.TrimSpace(c.Security.Recipient)); err != nil {
			return &ValidationError{Field: "security.recipient", Message: "must be a valid AGE X25519 recipient"}
		}
	}
	if !validIOSPath(c.IOS.Path) {
		return &ValidationError{Field: "ios.path", Message: "must be a clean relative path without traversal or backslashes"}
	}
	if !schemePattern.MatchString(c.IOS.Scheme) {
		return &ValidationError{Field: "ios.scheme", Message: "contains unsupported characters or is too long"}
	}
	if c.IOS.Configuration != "" && c.IOS.Configuration != "Debug" && c.IOS.Configuration != "Release" {
		return &ValidationError{Field: "ios.configuration", Message: "must be Debug or Release"}
	}
	if c.IOS.TestScript != "" {
		if err := ValidateTestScriptPath(c.IOS.TestScript); err != nil {
			return &ValidationError{Field: "ios.testScript", Message: err.Error()}
		}
	}
	if c.IOS.TestImage != "" {
		if err := ValidateTestImage(c.IOS.TestImage); err != nil {
			return &ValidationError{Field: "ios.testImage", Message: err.Error()}
		}
	}
	if c.IOS.BuildXcode != "" {
		if err := ValidateBuildXcode(c.IOS.BuildXcode); err != nil {
			return &ValidationError{Field: "ios.buildXcode", Message: err.Error()}
		}
	}
	if c.Windows.TestScript != "" {
		if err := ValidateWindowsTestScriptPath(c.Windows.TestScript); err != nil {
			return &ValidationError{Field: "windows.testScript", Message: err.Error()}
		}
	}
	if c.MacOS.BuildScript != "" {
		if err := ValidateTestScriptPath(c.MacOS.BuildScript); err != nil {
			return &ValidationError{Field: "macos.buildScript", Message: err.Error()}
		}
	}
	if c.MacOS.App != "" {
		if err := ValidateArtifactPath(c.MacOS.App); err != nil {
			return &ValidationError{Field: "macos.app", Message: err.Error()}
		}
		if path.Ext(c.MacOS.App) != ".app" || path.Base(c.MacOS.App) == ".app" {
			return &ValidationError{Field: "macos.app", Message: "must name an application bundle ending in .app"}
		}
	}
	if c.Windows.Artifact != "" {
		if err := ValidateArtifactPath(c.Windows.Artifact); err != nil {
			return &ValidationError{Field: "windows.artifact", Message: err.Error()}
		}
	}
	return nil
}

// ValidateTestScriptPath applies the central workflow's test_script rule
// locally, so a bad path fails before anything is snapshotted or dispatched:
// a clean path relative to the repository root, using forward slashes, with
// no traversal, Git metadata, control characters, or unusual characters.
func ValidateTestScriptPath(value string) error {
	return validateSnapshotFilePath(value, MaxTestScriptPathLength, "scripts/test.sh")
}

// ValidateWindowsTestScriptPath applies ValidateTestScriptPath and requires a
// .ps1 (run with pwsh) or .sh (run with Git for Windows bash) script.
func ValidateWindowsTestScriptPath(value string) error {
	if err := validateSnapshotFilePath(value, MaxTestScriptPathLength, "scripts/windows-test.ps1"); err != nil {
		return err
	}
	if ext := strings.ToLower(path.Ext(value)); ext != ".ps1" && ext != ".sh" {
		return errors.New("must end in .ps1 (run with pwsh) or .sh (run with bash)")
	}
	return nil
}

// ValidateArtifactPath applies the central workflow's artifact_path rule,
// which is the test_script rule, locally.
func ValidateArtifactPath(value string) error {
	return validateSnapshotFilePath(value, MaxArtifactPathLength, "dist/Setup.exe")
}

func validateSnapshotFilePath(value string, maxLength int, example string) error {
	switch {
	case value == "":
		return errors.New("is required")
	case len(value) > maxLength:
		return fmt.Errorf("is longer than %d bytes", maxLength)
	case strings.HasPrefix(value, "/"):
		return fmt.Errorf("must be relative to the repository root, like %s", example)
	case strings.Contains(value, `\`):
		return fmt.Errorf("must use forward slashes, like %s", example)
	case strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return errors.New("must not contain control characters")
	}
	for _, segment := range strings.Split(value, "/") {
		switch {
		case segment == "..":
			return errors.New("must stay inside the repository")
		case strings.EqualFold(segment, ".git"): // APFS and NTFS are case-insensitive by default
			return errors.New("must not point into Git metadata")
		case segment == "" || segment == ".":
			return errors.New("must be a clean path without ./, // or a trailing /")
		case !testScriptSegmentPattern.MatchString(segment):
			return errors.New("may contain only letters, digits, '.', '_', '+', '-' and '/', and no path segment may start with '-'")
		case strings.HasSuffix(segment, "."):
			// Windows drops a trailing '.', so ".git." would name ".git".
			return errors.New("must not have a path segment ending in '.'")
		}
	}
	if path.Clean(value) != value {
		return errors.New("must be a clean path without ./, // or a trailing /")
	}
	return nil
}

func validOwner(value string) bool {
	return ownerPattern.MatchString(value) && !strings.Contains(value, "--")
}

func validRepository(value string) bool {
	return repositoryPattern.MatchString(value) && value != "." && value != ".." && !strings.HasSuffix(strings.ToLower(value), ".git")
}

func validIOSPath(value string) bool {
	if value == "" || value == "." {
		return true
	}
	if strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\\r\n\x00") || len(value) > 512 {
		return false
	}
	return path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}

// ValidationError represents a configuration validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return "config validation error: " + e.Field + ": " + e.Message
}
