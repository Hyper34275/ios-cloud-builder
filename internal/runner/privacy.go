package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"filippo.io/age"
)

// maskedInputs are the dispatch inputs that name the private project: its
// owner and repository, and paths and names inside it. The public log would
// otherwise print them in every step header that receives them.
var maskedInputs = []string{"source_owner", "source_repo", "ios_path", "scheme", "test_script", "artifact_path"}

// genericInputValues are common enough that masking them would garble the
// public log without hiding anything about the project.
var genericInputValues = map[string]bool{
	".": true, "ios": true, "app": true, "App": true, "app/ios": true, "Runner": true,
	"macos": true, "windows": true, "android": true, "scripts": true,
	"dist": true, "build": true, "src": true, "test": true, "tests": true,
}

const maxEventBytes = 1024 * 1024

// MaskCommands reads the workflow_dispatch event and returns one ::add-mask::
// command for each private input value, its path components and its file
// name without the extension. Values come from the event file rather than an
// expression so the step that registers the masks never prints them itself.
func MaskCommands(eventPath string) ([]string, error) {
	file, err := os.Open(eventPath)
	if err != nil {
		return nil, fmt.Errorf("open workflow event")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxEventBytes+1))
	if err != nil || len(data) > maxEventBytes {
		return nil, fmt.Errorf("read workflow event")
	}
	var event struct {
		Inputs map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, fmt.Errorf("parse workflow event")
	}
	seen := map[string]bool{}
	var commands []string
	add := func(value string) {
		// A mask command is one line, so each line of a value is its own mask.
		for _, line := range strings.FieldsFunc(value, func(r rune) bool { return r == '\r' || r == '\n' }) {
			line = strings.TrimSpace(line)
			if len(line) < 3 || genericInputValues[line] || seen[line] {
				continue
			}
			seen[line] = true
			commands = append(commands, "::add-mask::"+line)
		}
	}
	for _, name := range maskedInputs {
		value, _ := event.Inputs[name].(string)
		add(value)
		for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' }) {
			add(part)
			if dot := strings.LastIndex(part, "."); dot > 0 {
				add(part[:dot])
			}
		}
	}
	return commands, nil
}

var buildCountPattern = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)

const maxSealedBuildNumberBytes = 4096

// SealedBuildNumberFile is the name of the sealed commit count the build job
// uploads for the signing job.
const SealedBuildNumberFile = "build-number.age"

// SealBuildNumber encrypts the snapshot's commit count to the protected
// signing identity and writes it to outputPath, so the count can cross from
// the build job to the signing job as an artifact without appearing in the
// public log. A job output would not do: GitHub drops an output whose value
// happens to contain a masked string.
func SealBuildNumber(count, recipientText, outputPath string) error {
	if !buildCountPattern.MatchString(count) {
		return fmt.Errorf("invalid build count")
	}
	if !filepath.IsAbs(outputPath) || filepath.Base(outputPath) != SealedBuildNumberFile {
		return fmt.Errorf("invalid sealed build number path")
	}
	recipient, err := age.ParseX25519Recipient(recipientText)
	if err != nil {
		return fmt.Errorf("parse signing AGE recipient")
	}
	var sealed bytes.Buffer
	writer, err := age.Encrypt(&sealed, recipient)
	if err != nil {
		return fmt.Errorf("seal build number")
	}
	if _, err := io.WriteString(writer, count); err != nil {
		return fmt.Errorf("seal build number")
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("seal build number")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0700); err != nil {
		return fmt.Errorf("prepare sealed build number")
	}
	if err := os.WriteFile(outputPath, sealed.Bytes(), 0600); err != nil {
		return fmt.Errorf("write sealed build number")
	}
	return nil
}

var buildAttemptPattern = regexp.MustCompile(`^[1-9][0-9]{0,5}$`)

// UnsealBuildNumber decrypts a SealBuildNumber file with the signing identity
// and appends the run attempt, giving the CFBundleVersion.
func UnsealBuildNumber(sealedPath, attempt, identityText string) (string, error) {
	if !buildAttemptPattern.MatchString(attempt) {
		return "", fmt.Errorf("invalid build attempt")
	}
	info, err := os.Lstat(sealedPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSealedBuildNumberBytes {
		return "", fmt.Errorf("sealed build number is missing or invalid")
	}
	ciphertext, err := os.ReadFile(sealedPath)
	if err != nil {
		return "", fmt.Errorf("read sealed build number")
	}
	identity, err := age.ParseX25519Identity(strings.TrimSpace(identityText))
	if err != nil {
		return "", fmt.Errorf("parse signing transport identity")
	}
	reader, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return "", fmt.Errorf("decrypt sealed build number")
	}
	count, err := io.ReadAll(io.LimitReader(reader, 32))
	if err != nil || !buildCountPattern.Match(count) {
		return "", fmt.Errorf("invalid sealed build number")
	}
	return string(count) + "." + attempt, nil
}
