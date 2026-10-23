package build

import (
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
)

func TestAddMacOSDispatchInputs(t *testing.T) {
	cfg := &config.Config{
		GitHub:   config.GitHubConfig{Owner: "source-owner", Repo: "private-app"},
		MacOS:    config.MacOSConfig{BuildScript: "scripts/build-app-store.sh", App: "dist/App.app"},
		Security: config.SecurityConfig{Recipient: "age1example"},
	}
	inputs := centralDispatchInputs(cfg, "id", "ref", true, "")
	if err := addMacOSDispatchInputs(inputs, cfg, true); err != nil {
		t.Fatal(err)
	}
	if inputs["operation"] != "testflight" || inputs["platform"] != "macos" ||
		inputs["test_script"] != "scripts/build-app-store.sh" || inputs["artifact_path"] != "dist/App.app" {
		t.Errorf("macOS dispatch inputs = %v", inputs)
	}
	if err := addMacOSDispatchInputs(centralDispatchInputs(cfg, "id", "ref", false, ""), cfg, false); err == nil {
		t.Error("an unsigned macOS build was dispatched")
	}
	cfg.MacOS.App = ""
	if err := addMacOSDispatchInputs(centralDispatchInputs(cfg, "id", "ref", true, ""), cfg, true); err == nil {
		t.Error("a macOS deployment without macos.app was dispatched")
	}
}

func TestParseEncryptedArtifactInstallerIdentity(t *testing.T) {
	got, err := parseEncryptedArtifact(makeZIP(t, map[string][]byte{
		centralLogFile:       []byte("log-ciphertext"),
		centralInstallerFile: []byte("installer-ciphertext"),
	}))
	if err != nil || string(got.installer) != "installer-ciphertext" || len(got.ipa) != 0 {
		t.Fatalf("parseEncryptedArtifact(installer) = %#v, %v", got, err)
	}
	if _, err := parseEncryptedArtifact(makeZIP(t, map[string][]byte{
		centralLogFile:       []byte("log-ciphertext"),
		centralIPAFile:       []byte("ipa-ciphertext"),
		centralInstallerFile: []byte("installer-ciphertext"),
	})); err == nil || !strings.Contains(err.Error(), "mixes") {
		t.Errorf("an IPA artifact carrying an installer identity was accepted: %v", err)
	}
}
