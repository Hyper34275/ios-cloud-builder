package main

import (
	"os"
	"path/filepath"
	"testing"
)

// chdir moves into a fresh temp directory for the duration of the test, since
// isKMPProject scans the working directory.
func chdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestIsKMPProject(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{
			name:  "kotlin dsl in root build file",
			files: map[string]string{"build.gradle.kts": "plugins {\n  kotlin(\"multiplatform\")\n}\n"},
			want:  true,
		},
		{
			name:  "plugin in a module build file",
			files: map[string]string{"shared/build.gradle.kts": "plugins {\n  kotlin(\"multiplatform\")\n}\n"},
			want:  true,
		},
		{
			name:  "groovy plugin id",
			files: map[string]string{"shared/build.gradle": "apply plugin: 'org.jetbrains.kotlin.multiplatform'\n"},
			want:  true,
		},
		{
			name:  "plugins block with id()",
			files: map[string]string{"build.gradle.kts": "plugins {\n  id(\"org.jetbrains.kotlin.multiplatform\") version \"2.0.0\"\n}\n"},
			want:  true,
		},
		{
			// How most current KMP projects declare it: the id lives in the
			// version catalog and build files only reference the alias.
			name: "version catalog alias",
			files: map[string]string{
				"gradle/libs.versions.toml": "[plugins]\nkotlin-multiplatform = { id = \"org.jetbrains.kotlin.multiplatform\", version.ref = \"kotlin\" }\n",
				"shared/build.gradle.kts":   "plugins {\n  alias(libs.plugins.kotlin.multiplatform)\n}\n",
			},
			want: true,
		},
		{
			// A catalog can mention unrelated libraries whose names contain
			// "multiplatform" (multiplatform-settings and friends).
			name: "catalog with only multiplatform-named libraries",
			files: map[string]string{
				"gradle/libs.versions.toml": "[versions]\nmultiplatformSettings = \"1.3.0\"\n[libraries]\nsettings = { module = \"com.russhwolf:multiplatform-settings\" }\n",
				"build.gradle.kts":          "plugins {\n  kotlin(\"jvm\")\n}\n",
			},
			want: false,
		},
		{
			name:  "android-only gradle project",
			files: map[string]string{"build.gradle.kts": "plugins {\n  id(\"com.android.application\")\n  kotlin(\"android\")\n}\n"},
			want:  false,
		},
		{
			// The bare word appears in prose and dependency names far more often
			// than the plugin itself; matching it would misconfigure the build.
			name:  "word multiplatform in a comment",
			files: map[string]string{"build.gradle.kts": "// we may go multiplatform one day\nplugins {\n  kotlin(\"jvm\")\n}\n"},
			want:  false,
		},
		{
			name:  "no gradle files at all",
			files: map[string]string{"package.json": `{"name":"app"}`},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := chdir(t)
			for name, content := range tt.files {
				writeFile(t, filepath.Join(dir, name), content)
			}
			if got := isKMPProject(); got != tt.want {
				t.Errorf("isKMPProject() = %v, want %v", got, tt.want)
			}
		})
	}
}
