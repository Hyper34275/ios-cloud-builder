package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// DartDefinesFile is where the CLI's snapshot carries flutter.dartDefines. The
// values travel inside the private snapshot, never as a workflow input, so
// they never reach the public log.
const DartDefinesFile = ".ios-builder/dart-defines.json"

const (
	maxDartDefines        = 32
	maxDartDefineValueLen = 2048
	maxDartDefinesBytes   = 128 * 1024
)

var dartDefineKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// readDartDefines returns the snapshot's dart-defines as sorted KEY=VALUE
// pairs, or none when the snapshot has no dart-defines file. It runs before
// any project code, and rejects a symlinked or oversized file, so a project
// cannot point it elsewhere.
func readDartDefines(sourceRoot string) ([]string, error) {
	for _, name := range []string{filepath.Dir(DartDefinesFile), DartDefinesFile} {
		info, err := os.Lstat(filepath.Join(sourceRoot, filepath.FromSlash(name)))
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("dart-defines file is not a plain file")
		}
		if name == DartDefinesFile && (!info.Mode().IsRegular() || info.Size() > maxDartDefinesBytes) {
			return nil, fmt.Errorf("dart-defines file is not a plain file within the size limit")
		}
	}
	file, err := os.Open(filepath.Join(sourceRoot, filepath.FromSlash(DartDefinesFile)))
	if err != nil {
		return nil, fmt.Errorf("open dart-defines file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxDartDefinesBytes+1))
	if err != nil || len(data) > maxDartDefinesBytes {
		return nil, fmt.Errorf("read dart-defines file")
	}
	var defines map[string]string
	if err := json.Unmarshal(data, &defines); err != nil {
		return nil, fmt.Errorf("dart-defines file is not a JSON object of strings")
	}
	if len(defines) > maxDartDefines {
		return nil, fmt.Errorf("too many dart-defines (max %d)", maxDartDefines)
	}
	pairs := make([]string, 0, len(defines))
	for key, value := range defines {
		if !dartDefineKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid dart-define key")
		}
		if value == "" || len(value) > maxDartDefineValueLen || containsControl(value) {
			return nil, fmt.Errorf("invalid dart-define value")
		}
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)
	return pairs, nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// flutterBuildArgs is the `flutter build ios` argv with one discrete
// --dart-define=KEY=VALUE element per pair, so no shell parsing is involved.
func flutterBuildArgs(mode string, dartDefines []string) []string {
	args := make([]string, 0, 4+len(dartDefines))
	args = append(args, "build", "ios", mode)
	for _, define := range dartDefines {
		args = append(args, "--dart-define="+define)
	}
	return append(args, "--no-codesign")
}
