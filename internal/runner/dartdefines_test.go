package runner

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeDartDefines(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".ios-builder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(DartDefinesFile)), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadDartDefines(t *testing.T) {
	root := writeDartDefines(t, `{"FLAG":"on","API_URL":"https://api.example.com/a,b"}`)
	got, err := readDartDefines(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"API_URL=https://api.example.com/a,b", "FLAG=on"}; !reflect.DeepEqual(got, want) {
		t.Errorf("readDartDefines = %q, want %q", got, want)
	}
	if args := flutterBuildArgs("--release", got); !reflect.DeepEqual(args, []string{
		"build", "ios", "--release", "--dart-define=API_URL=https://api.example.com/a,b", "--dart-define=FLAG=on", "--no-codesign",
	}) {
		t.Errorf("flutterBuildArgs = %q", args)
	}
	if got, err := readDartDefines(t.TempDir()); err != nil || got != nil {
		t.Errorf("no file: %q, %v; want none", got, err)
	}
}

func TestReadDartDefinesRejectsUnsafeFiles(t *testing.T) {
	for name, contents := range map[string]string{
		"not an object":   `["A=1"]`,
		"non-string":      `{"A":1}`,
		"bad key":         `{"1A":"x"}`,
		"flag-like key":   `{"--target":"x"}`,
		"empty value":     `{"A":""}`,
		"control value":   `{"A":"x\ny"}`,
		"oversized value": `{"A":"` + strings.Repeat("x", maxDartDefineValueLen+1) + `"}`,
	} {
		if _, err := readDartDefines(writeDartDefines(t, contents)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	many := make([]string, 0, maxDartDefines+1)
	for i := 0; i <= maxDartDefines; i++ {
		many = append(many, `"K`+strings.Repeat("x", i)+`":"v"`)
	}
	if _, err := readDartDefines(writeDartDefines(t, "{"+strings.Join(many, ",")+"}")); err == nil {
		t.Error("too many entries accepted")
	}

	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"A":"1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".ios-builder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(DartDefinesFile))); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readDartDefines(root); err == nil {
		t.Error("symlinked dart-defines file accepted")
	}
}
