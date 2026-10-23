package runner

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
		value = strings.TrimSpace(value)
		if len(value) < 3 || genericInputValues[value] || seen[value] || strings.ContainsAny(value, "\r\n") {
			return
		}
		seen[value] = true
		commands = append(commands, "::add-mask::"+value)
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

// SealBuildNumber encrypts the snapshot's commit count to the protected
// signing identity and returns it base64-encoded, so the count can cross from
// the build job to the signing job as a job output without appearing in the
// public log.
func SealBuildNumber(count, recipientText string) (string, error) {
	if !buildCountPattern.MatchString(count) {
		return "", fmt.Errorf("invalid build count")
	}
	recipient, err := age.ParseX25519Recipient(recipientText)
	if err != nil {
		return "", fmt.Errorf("parse signing AGE recipient")
	}
	var sealed bytes.Buffer
	writer, err := age.Encrypt(&sealed, recipient)
	if err != nil {
		return "", fmt.Errorf("seal build number")
	}
	if _, err := io.WriteString(writer, count); err != nil {
		return "", fmt.Errorf("seal build number")
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("seal build number")
	}
	return base64.StdEncoding.EncodeToString(sealed.Bytes()), nil
}

var buildAttemptPattern = regexp.MustCompile(`^[1-9][0-9]{0,5}$`)

// UnsealBuildNumber decrypts a SealBuildNumber value with the signing
// identity and appends the run attempt, giving the CFBundleVersion.
func UnsealBuildNumber(sealed, attempt, identityText string) (string, error) {
	if !buildAttemptPattern.MatchString(attempt) {
		return "", fmt.Errorf("invalid build attempt")
	}
	if len(sealed) > maxSealedBuildNumberBytes {
		return "", fmt.Errorf("invalid sealed build number")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("invalid sealed build number")
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
