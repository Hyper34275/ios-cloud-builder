package runner

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var errNoFlutterProject = errors.New("no pubspec.yaml beside the iOS project or at the checkout root")

// DetectFramework inspects manifests without executing private project code.
// iosPath is the checkout-relative iOS project directory, which locates a
// Flutter application that does not sit at the checkout root.
func DetectFramework(sourceRoot, iosPath, hint string) (string, error) {
	if !validFramework(hint) {
		return "", os.ErrInvalid
	}
	if hint != FrameworkAuto {
		return hint, nil
	}
	if _, err := FlutterProjectRoot(sourceRoot, iosPath); err == nil {
		return FrameworkFlutter, nil
	}
	pkg, _ := os.ReadFile(filepath.Join(sourceRoot, "package.json"))
	switch {
	case bytes.Contains(pkg, []byte(`"expo"`)):
		return FrameworkExpo, nil
	case bytes.Contains(pkg, []byte(`"react-native"`)):
		return FrameworkReactNative, nil
	case bytes.Contains(pkg, []byte(`"@ionic/`)) || bytes.Contains(pkg, []byte(`"ionic"`)):
		return FrameworkIonic, nil
	case bytes.Contains(pkg, []byte(`"cordova"`)) || exists(filepath.Join(sourceRoot, "config.xml")):
		return FrameworkCordova, nil
	}
	foundKMP := false
	_ = filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || foundKMP {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if path != sourceRoot && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" || entry.Name() == "Pods") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "build.gradle" && entry.Name() != "build.gradle.kts" && entry.Name() != "libs.versions.toml" {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr == nil && (bytes.Contains(contents, []byte("kotlin(\"multiplatform\")")) ||
			bytes.Contains(contents, []byte("org.jetbrains.kotlin.multiplatform"))) {
			foundKMP = true
			return filepath.SkipAll
		}
		return nil
	})
	if foundKMP {
		return FrameworkKMP, nil
	}
	return FrameworkNative, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// FlutterProjectRoot resolves the directory holding pubspec.yaml, which is
// where every flutter command has to run and where it writes build/. Flutter
// generates its iOS project as <root>/ios, so the parent of the iOS path is
// the project root whenever a repository nests the application in a
// subdirectory. A conventional single-application repository keeps pubspec.yaml
// at the checkout root, where the parent of <root>/ios also lands, so both
// layouts resolve through the same rule.
//
// iosPath is relative to sourceRoot and may be empty. A candidate outside the
// checkout is ignored rather than rejected, so a malformed path can never
// direct a build at a directory the snapshot does not contain.
func FlutterProjectRoot(sourceRoot, iosPath string) (string, error) {
	candidates := make([]string, 0, 2)
	if iosPath != "" {
		candidates = append(candidates, filepath.Dir(filepath.Join(sourceRoot, iosPath)))
	}
	candidates = append(candidates, sourceRoot)
	for _, candidate := range candidates {
		if pathWithin(sourceRoot, candidate) && exists(filepath.Join(candidate, "pubspec.yaml")) {
			return candidate, nil
		}
	}
	return "", errNoFlutterProject
}
