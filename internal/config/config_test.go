package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		wantErr  bool
		errField string
	}{
		{
			name: "valid config",
			config: Config{
				Project:  "MyApp",
				Platform: "ios",
				GitHub:   GitHubConfig{Owner: "user", Repo: "builder-myapp"},
			},
			wantErr: false,
		},
		{
			name: "missing project",
			config: Config{
				Platform: "ios",
				GitHub:   GitHubConfig{Owner: "user", Repo: "builder-myapp"},
			},
			wantErr:  true,
			errField: "project",
		},
		{
			name: "missing github owner",
			config: Config{
				Project:  "MyApp",
				Platform: "ios",
				GitHub:   GitHubConfig{Repo: "builder-myapp"},
			},
			wantErr:  true,
			errField: "github.owner",
		},
		{
			name: "missing github repo",
			config: Config{
				Project:  "MyApp",
				Platform: "ios",
				GitHub:   GitHubConfig{Owner: "user"},
			},
			wantErr:  true,
			errField: "github.repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil {
				valErr, ok := err.(*ValidationError)
				if !ok {
					t.Errorf("Validate() error type = %T, want *ValidationError", err)
					return
				}
				if valErr.Field != tt.errField {
					t.Errorf("Validate() error field = %q, want %q", valErr.Field, tt.errField)
				}
			}
		})
	}
}

func TestManager_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "builder.json")

	mgr := &Manager{path: configPath}

	// Create config
	cfg := &Config{
		Project:  "TestApp",
		Platform: "ios",
		GitHub: GitHubConfig{
			Owner: "testuser",
			Repo:  "builder-testapp",
		},
	}

	// Save
	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("Config file not created after Save()")
	}

	// Load
	loaded, err := mgr.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify fields
	if loaded.Project != cfg.Project {
		t.Errorf("Project = %q, want %q", loaded.Project, cfg.Project)
	}
	if loaded.Platform != cfg.Platform {
		t.Errorf("Platform = %q, want %q", loaded.Platform, cfg.Platform)
	}
	if loaded.GitHub.Owner != cfg.GitHub.Owner {
		t.Errorf("GitHub.Owner = %q, want %q", loaded.GitHub.Owner, cfg.GitHub.Owner)
	}
	if loaded.GitHub.Repo != cfg.GitHub.Repo {
		t.Errorf("GitHub.Repo = %q, want %q", loaded.GitHub.Repo, cfg.GitHub.Repo)
	}
}

func TestManager_Load_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "nonexistent.json")

	mgr := &Manager{path: configPath}

	_, err := mgr.Load()
	if err != ErrConfigNotFound {
		t.Errorf("Load() error = %v, want ErrConfigNotFound", err)
	}
}

func TestNewManager(t *testing.T) {
	mgr := NewManager()
	if mgr.path != ConfigFileName {
		t.Errorf("NewManager().path = %q, want %q", mgr.path, ConfigFileName)
	}
}

func TestManager_LoadMigratesLegacyConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "builder.json")
	legacy := []byte(`{
  "project": "LegacyApp",
  "platform": "ios",
  "github": {"owner": "source-owner", "repo": "private-app"}
}`)
	if err := os.WriteFile(configPath, legacy, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := (&Manager{path: configPath}).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Backend != BackendRepository {
		t.Errorf("Backend = %q, want %q", cfg.Backend, BackendRepository)
	}
	if cfg.IsCentral() {
		t.Error("legacy config unexpectedly migrated to central mode")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("migrated legacy Validate() error = %v", err)
	}
}

func TestConfig_CentralDefaultsAndRoundTrip(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Project:  "PrivateApp",
		Platform: "ios",
		Backend:  BackendCentral,
		GitHub:   GitHubConfig{Owner: "source-owner", Repo: "private-app"},
		Builder:  BuilderConfig{Owner: "builder-owner", Repo: "ios-cloud-builder"},
		Security: SecurityConfig{Recipient: identity.Recipient().String()},
	}
	if changed := cfg.ApplyDefaults(); !changed {
		t.Fatal("ApplyDefaults() = false, want central workflow default")
	}
	if cfg.Builder.Workflow != DefaultWorkflow {
		t.Errorf("Builder.Workflow = %q, want %q", cfg.Builder.Workflow, DefaultWorkflow)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Config
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.GitHub.Repo != "private-app" || roundTrip.Builder.Repo != "ios-cloud-builder" {
		t.Fatalf("source/builder repositories not preserved: %#v", roundTrip)
	}
}

func TestConfig_MigratesPreBackendCentralConfig(t *testing.T) {
	cfg := Config{Builder: BuilderConfig{Owner: "builder", Repo: "public"}}
	if !cfg.Migrate() {
		t.Fatal("Migrate() = false, want changes")
	}
	if cfg.Backend != BackendCentral {
		t.Errorf("Backend = %q, want %q", cfg.Backend, BackendCentral)
	}
	if cfg.Builder.Workflow != DefaultWorkflow {
		t.Errorf("Builder.Workflow = %q, want %q", cfg.Builder.Workflow, DefaultWorkflow)
	}
}

func TestConfig_CentralValidation(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	valid := Config{
		Project:  "App",
		Platform: "ios",
		Backend:  BackendCentral,
		GitHub:   GitHubConfig{Owner: "source", Repo: "private"},
		Builder:  BuilderConfig{Owner: "builder", Repo: "public", Workflow: DefaultWorkflow},
		Security: SecurityConfig{Recipient: identity.Recipient().String()},
	}

	tests := []struct {
		name  string
		field string
		edit  func(*Config)
	}{
		{name: "builder owner", field: "builder.owner", edit: func(c *Config) { c.Builder.Owner = "" }},
		{name: "builder repo", field: "builder.repo", edit: func(c *Config) { c.Builder.Repo = "" }},
		{name: "builder workflow", field: "builder.workflow", edit: func(c *Config) { c.Builder.Workflow = "" }},
		{name: "workflow path", field: "builder.workflow", edit: func(c *Config) { c.Builder.Workflow = "workflows/ios.yml" }},
		{name: "recipient missing", field: "security.recipient", edit: func(c *Config) { c.Security.Recipient = "" }},
		{name: "recipient invalid", field: "security.recipient", edit: func(c *Config) { c.Security.Recipient = "AGE-SECRET-KEY-NOT-PUBLIC" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.edit(&cfg)
			err := cfg.Validate()
			validationErr, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("Validate() error = %v (%T), want *ValidationError", err, err)
			}
			if validationErr.Field != tt.field {
				t.Errorf("field = %q, want %q", validationErr.Field, tt.field)
			}
		})
	}
}

func TestConfig_DartDefinesRoundTrip(t *testing.T) {
	cfg := &Config{
		Project:  "App",
		Platform: "ios",
		GitHub:   GitHubConfig{Owner: "owner", Repo: "repo"},
		Flutter: FlutterConfig{
			DartDefines: map[string]string{
				"API_URL":   "https://api.example.com",
				"FEATURE_X": "true",
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"dartDefines"`) {
		t.Fatalf("marshaled config missing dartDefines: %s", data)
	}

	var roundTrip Config
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip.Flutter.DartDefines, cfg.Flutter.DartDefines) {
		t.Fatalf("DartDefines = %#v, want %#v", roundTrip.Flutter.DartDefines, cfg.Flutter.DartDefines)
	}

	empty := &Config{Project: "App", Platform: "ios", GitHub: GitHubConfig{Owner: "owner", Repo: "repo"}}
	data, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "dartDefines") {
		t.Fatalf("empty dartDefines should be omitted: %s", data)
	}
}

func TestConfig_DartDefinesValidation(t *testing.T) {
	base := Config{
		Project:  "App",
		Platform: "ios",
		GitHub:   GitHubConfig{Owner: "owner", Repo: "repo"},
	}
	tests := []struct {
		name    string
		defines map[string]string
	}{
		{"key starts with digit", map[string]string{"1KEY": "value"}},
		{"key contains dash", map[string]string{"MY-KEY": "value"}},
		{"key contains space", map[string]string{"MY KEY": "value"}},
		{"key contains dot", map[string]string{"MY.KEY": "value"}},
		{"empty key", map[string]string{"": "value"}},
		{"empty value", map[string]string{"KEY": ""}},
		{"value with newline", map[string]string{"KEY": "line1\nline2"}},
		{"value with carriage return", map[string]string{"KEY": "line1\rline2"}},
		{"value with NUL", map[string]string{"KEY": "a\x00b"}},
		{"oversized value", map[string]string{"KEY": strings.Repeat("a", 2049)}},
		{"too many entries", makeDartDefines(33)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			cfg.Flutter = FlutterConfig{DartDefines: tt.defines}
			err := cfg.Validate()
			valErr, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("Validate() error = %v (%T), want *ValidationError", err, err)
			}
			if valErr.Field != "flutter.dartDefines" {
				t.Errorf("field = %q, want flutter.dartDefines", valErr.Field)
			}
		})
	}

	valid := base
	valid.Flutter = FlutterConfig{DartDefines: map[string]string{
		"API_URL":   "https://api.example.com",
		"_INTERNAL": "1",
		"maxLen":    strings.Repeat("a", 2048),
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}

	atLimit := base
	atLimit.Flutter = FlutterConfig{DartDefines: makeDartDefines(maxDartDefines)}
	if err := atLimit.Validate(); err != nil {
		t.Fatalf("Validate() at max entries error = %v, want nil", err)
	}
}

func makeDartDefines(n int) map[string]string {
	m := make(map[string]string, n)
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("KEY_%d", i)] = "value"
	}
	return m
}

func TestConfig_RepositoryModeDoesNotRequireCentralFields(t *testing.T) {
	cfg := Config{
		Project: "Legacy",
		Backend: BackendRepository,
		GitHub:  GitHubConfig{Owner: "owner", Repo: "repo"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfig_TestScript(t *testing.T) {
	base := Config{Project: "App", GitHub: GitHubConfig{Owner: "owner", Repo: "repo"}}
	for _, script := range []string{"", "scripts/ios-test.sh", ".ci/test.sh", "ios/Scripts/run_tests+ui.sh"} {
		cfg := base
		cfg.IOS.TestScript = script
		if err := cfg.Validate(); err != nil {
			t.Errorf("ios.testScript %q rejected: %v", script, err)
		}
	}
	for _, script := range []string{"/abs/test.sh", "../test.sh", "./test.sh", `scripts\test.sh`, "scripts//test.sh", ".git/hooks/x", "-x.sh", "test.sh\n", "my test.sh", "$(id).sh"} {
		cfg := base
		cfg.IOS.TestScript = script
		err := cfg.Validate()
		validationErr, ok := err.(*ValidationError)
		if !ok || validationErr.Field != "ios.testScript" {
			t.Errorf("ios.testScript %q: Validate() = %v, want an ios.testScript error", script, err)
		}
	}
}

func TestConfig_TestImage(t *testing.T) {
	base := Config{Project: "App", GitHub: GitHubConfig{Owner: "owner", Repo: "repo"}}
	for _, image := range []string{"", "macos-26", "xcode-27"} {
		cfg := base
		cfg.IOS.TestImage = image
		if err := cfg.Validate(); err != nil {
			t.Errorf("ios.testImage %q rejected: %v", image, err)
		}
	}
	for _, image := range []string{"xcode-27-xlarge", "macos-26-xlarge", "macos-15", "ubuntu-latest", "self-hosted", "Xcode-27", " xcode-27", "xcode-27\n"} {
		cfg := base
		cfg.IOS.TestImage = image
		err := cfg.Validate()
		validationErr, ok := err.(*ValidationError)
		if !ok || validationErr.Field != "ios.testImage" {
			t.Errorf("ios.testImage %q: Validate() = %v, want an ios.testImage error", image, err)
		}
	}
	if got := TestImages(); len(got) != 2 || got[0] != TestImageMacOS26 || got[1] != TestImageXcode27 {
		t.Errorf("TestImages() = %v", got)
	}
}

func TestConfig_BuildXcode(t *testing.T) {
	base := Config{Project: "App", GitHub: GitHubConfig{Owner: "owner", Repo: "repo"}}
	for _, xcode := range []string{"", "26.3", "27.1"} {
		cfg := base
		cfg.IOS.BuildXcode = xcode
		if err := cfg.Validate(); err != nil {
			t.Errorf("ios.buildXcode %q rejected: %v", xcode, err)
		}
	}
	for _, xcode := range []string{"27.0", "27.2", "26", "27", "xcode-27", "macos-15", " 27.1", "27.1\n", "27.1_Release_Candidate"} {
		cfg := base
		cfg.IOS.BuildXcode = xcode
		err := cfg.Validate()
		validationErr, ok := err.(*ValidationError)
		if !ok || validationErr.Field != "ios.buildXcode" {
			t.Errorf("ios.buildXcode %q: Validate() = %v, want an ios.buildXcode error", xcode, err)
		}
	}
	if got := BuildXcodes(); len(got) != 2 || got[0] != BuildXcode263 || got[1] != BuildXcode271 {
		t.Errorf("BuildXcodes() = %v", got)
	}
}

func TestManager_TestScriptRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builder.json")
	if err := os.WriteFile(path, []byte(`{
  "project": "App",
  "platform": "ios",
  "github": {"owner": "owner", "repo": "repo"},
  "ios": {"path": "ios", "testScript": "scripts/ios-test.sh"}
}`), 0644); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{path: path}
	cfg, err := mgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IOS.TestScript != "scripts/ios-test.sh" {
		t.Fatalf("IOS.TestScript = %q", cfg.IOS.TestScript)
	}
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) || !bytes.Contains(data, []byte(`"testScript": "scripts/ios-test.sh"`)) {
		t.Fatalf("saved builder.json = %s", data)
	}
	cfg.IOS.TestScript = ""
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); bytes.Contains(data, []byte("testScript")) {
		t.Fatalf("an unset testScript was written: %s", data)
	}
}

func TestManager_BuildXcodeRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builder.json")
	if err := os.WriteFile(path, []byte(`{
  "project": "App",
  "platform": "ios",
  "github": {"owner": "owner", "repo": "repo"},
  "ios": {"path": "ios", "buildXcode": "27.1"}
}`), 0644); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{path: path}
	cfg, err := mgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IOS.BuildXcode != "27.1" {
		t.Fatalf("IOS.BuildXcode = %q", cfg.IOS.BuildXcode)
	}
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); !bytes.Contains(data, []byte(`"buildXcode": "27.1"`)) {
		t.Fatalf("saved builder.json = %s", data)
	}
	cfg.IOS.BuildXcode = ""
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); bytes.Contains(data, []byte("buildXcode")) {
		t.Fatalf("an unset buildXcode was written: %s", data)
	}
}

func TestConfig_WindowsSection(t *testing.T) {
	base := Config{Project: "App", GitHub: GitHubConfig{Owner: "owner", Repo: "repo"}}
	for _, windows := range []WindowsConfig{
		{},
		{TestScript: "scripts/windows-test.ps1"},
		{TestScript: "scripts/Windows-Test.PS1", Artifact: "dist/AppSetup.exe"},
		{TestScript: "ci/windows.sh", Artifact: "out/Release/App+Tools_1.2.msi"},
		{Artifact: "dist/AppSetup.exe"},
	} {
		cfg := base
		cfg.Windows = windows
		if err := cfg.Validate(); err != nil {
			t.Errorf("windows %+v rejected: %v", windows, err)
		}
	}
	for field, windows := range map[string][]WindowsConfig{
		"windows.testScript": {
			{TestScript: "scripts/windows-test.cmd"}, {TestScript: "scripts/windows-test"}, {TestScript: "../test.ps1"},
			{TestScript: `scripts\test.ps1`}, {TestScript: ".git/hooks/x.ps1"}, {TestScript: "scripts./test.ps1"},
		},
		"windows.artifact": {
			{Artifact: "../AppSetup.exe"}, {Artifact: "/dist/AppSetup.exe"}, {Artifact: `dist\AppSetup.exe`},
			{Artifact: "dist/App Setup.exe"}, {Artifact: "dist/AppSetup.exe."}, {Artifact: ".git/config"},
			{Artifact: "dist/Setup.exe:stream"},
		},
	} {
		for _, value := range windows {
			cfg := base
			cfg.Windows = value
			var validationErr *ValidationError
			if err := cfg.Validate(); !errors.As(err, &validationErr) || validationErr.Field != field {
				t.Errorf("windows %+v: Validate() = %v, want a %s error", value, err, field)
			}
		}
	}
}

func TestManager_WindowsSectionIsBackwardsCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builder.json")
	mgr := &Manager{path: path}
	write := func(contents string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// An existing configuration without the section loads, validates, and
	// saves without gaining one.
	write(`{"project": "App", "platform": "ios", "github": {"owner": "owner", "repo": "repo"}, "ios": {"testScript": "scripts/ios-test.sh"}}`)
	cfg, err := mgr.Load()
	if err != nil || cfg.Validate() != nil || cfg.Windows != (WindowsConfig{}) {
		t.Fatalf("Load(no windows section) = %+v, %v", cfg, err)
	}
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); bytes.Contains(data, []byte(`"windows"`)) {
		t.Fatalf("saving added an empty windows section: %s", data)
	}

	// The section round-trips, and sections this version does not know are
	// ignored rather than rejected.
	write(`{
  "project": "App",
  "platform": "ios",
  "github": {"owner": "owner", "repo": "repo"},
  "windows": {"testScript": "scripts/windows-test.ps1", "artifact": "dist/AppSetup.exe", "futureKey": true},
  "android": {"gradle": "app"}
}`)
	cfg, err = mgr.Load()
	if err != nil || cfg.Validate() != nil {
		t.Fatalf("Load(windows section) = %+v, %v", cfg, err)
	}
	if cfg.Windows != (WindowsConfig{TestScript: "scripts/windows-test.ps1", Artifact: "dist/AppSetup.exe"}) {
		t.Fatalf("Windows = %+v", cfg.Windows)
	}
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte(`"testScript": "scripts/windows-test.ps1"`)) || !bytes.Contains(data, []byte(`"artifact": "dist/AppSetup.exe"`)) {
		t.Fatalf("saved builder.json = %s, %v", data, err)
	}
}

func TestValidateMacOSConfig(t *testing.T) {
	valid := Config{
		Project: "App", GitHub: GitHubConfig{Owner: "owner", Repo: "repo"},
		MacOS: MacOSConfig{BuildScript: "scripts/build-app-store.sh", App: "dist/App.app"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid macOS configuration rejected: %v", err)
	}
	for name, macOS := range map[string]MacOSConfig{
		"traversing script": {BuildScript: "../build.sh", App: "dist/App.app"},
		"not a bundle":      {BuildScript: "build.sh", App: "dist/App.zip"},
		"absolute bundle":   {BuildScript: "build.sh", App: "/tmp/App.app"},
	} {
		cfg := valid
		cfg.MacOS = macOS
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
