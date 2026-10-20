package runner

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"howett.net/plist"
)

func TestProfileMatchesBundle(t *testing.T) {
	for _, test := range []struct {
		applicationID string
		bundleID      string
		want          bool
	}{
		{"TEAM123456.com.example.app", "com.example.app", true},
		{"TEAM123456.com.example.*", "com.example.app", false},
		{"OTHER12345.com.example.app", "com.example.app", false},
		{"TEAM123456.com.other.app", "com.example.app", false},
	} {
		if got := profileMatchesBundle(test.applicationID, "TEAM123456", test.bundleID); got != test.want {
			t.Fatalf("profileMatchesBundle(%q, %q) = %v", test.applicationID, test.bundleID, got)
		}
	}
}

func TestValidateAppStoreProfile(t *testing.T) {
	valid := provisioningProfile{
		ExpirationDate:        time.Now().Add(time.Hour),
		Platform:              []string{"iOS"},
		DeveloperCertificates: [][]byte{[]byte("certificate")},
		Entitlements:          map[string]any{"get-task-allow": false},
	}
	if err := validateAppStoreProfile(&valid); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	for name, mutate := range map[string]func(*provisioningProfile){
		"expired":      func(profile *provisioningProfile) { profile.ExpirationDate = time.Now().Add(-time.Minute) },
		"wrong target": func(profile *provisioningProfile) { profile.Platform = []string{"tvOS"} },
		"devices":      func(profile *provisioningProfile) { profile.ProvisionedDevices = []string{"device"} },
		"enterprise":   func(profile *provisioningProfile) { profile.ProvisionsAllDevices = true },
		"debuggable":   func(profile *provisioningProfile) { profile.Entitlements["get-task-allow"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			profile := valid
			profile.Platform = append([]string(nil), valid.Platform...)
			profile.DeveloperCertificates = append([][]byte(nil), valid.DeveloperCertificates...)
			profile.Entitlements = map[string]any{"get-task-allow": false}
			mutate(&profile)
			if err := validateAppStoreProfile(&profile); err == nil {
				t.Fatal("unsafe profile was accepted")
			}
		})
	}
}

func TestProfileAuthorizesIdentity(t *testing.T) {
	certificate := []byte("certificate DER")
	fingerprint := sha1.Sum(certificate) // #nosec G401 -- mirrors Apple's certificate fingerprint.
	identity := strings.ToUpper(hex.EncodeToString(fingerprint[:]))
	if !profileAuthorizesIdentity([][]byte{certificate}, identity) {
		t.Fatal("profile certificate was not matched")
	}
	if profileAuthorizesIdentity([][]byte{[]byte("other")}, identity) {
		t.Fatal("unrelated certificate was matched")
	}
}

func TestSigningIdentityPatternCapturesFingerprintAndName(t *testing.T) {
	output := `  1) 3A1C0EDBC4757C7C1CECE2469D5A878EEA8C2292 "Apple Distribution: Example (TEAM123456)"`
	match := identityPattern.FindStringSubmatch(output)
	if len(match) != 3 || match[1] != "3A1C0EDBC4757C7C1CECE2469D5A878EEA8C2292" ||
		match[2] != "Apple Distribution: Example (TEAM123456)" {
		t.Fatalf("identity match = %#v", match)
	}
}

func TestExtractUnsignedIPARejectsTraversalAndSymlink(t *testing.T) {
	for name, entries := range map[string]map[string]zipEntry{
		"traversal": {"Payload/App.app/../../secret": {data: "x", mode: 0600}},
		"symlink":   {"Payload/App.app/link": {data: "target", mode: os.ModeSymlink | 0777}},
	} {
		t.Run(name, func(t *testing.T) {
			ipa := writeDeployTestZIP(t, entries)
			if _, err := extractUnsignedIPA(ipa, filepath.Join(t.TempDir(), "out")); err == nil {
				t.Fatal("unsafe IPA was accepted")
			}
		})
	}
}

func TestExtractUnsignedIPAFindsOneApplication(t *testing.T) {
	ipa := writeDeployTestZIP(t, map[string]zipEntry{
		"Payload/":                   {mode: os.ModeDir | 0755},
		"Payload/App.app/":           {mode: os.ModeDir | 0755},
		"Payload/App.app/Info.plist": {data: "plist", mode: 0600},
		"Payload/App.app/App":        {data: "binary", mode: 0700},
	})
	root := filepath.Join(t.TempDir(), "out")
	app, err := extractUnsignedIPA(ipa, root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(app) != "App.app" {
		t.Fatalf("app path = %q", app)
	}
}

func TestTakeAppleCredentialsUnsetsEnvironment(t *testing.T) {
	values := map[string]string{
		"APPLE_SIGNING_AGE_IDENTITY":      "AGE-SECRET-KEY-TEST",
		"APPLE_DISTRIBUTION_P12":          "cDEy",
		"APPLE_DISTRIBUTION_P12_PASSWORD": " password ",
		"APPLE_PROVISIONING_PROFILE":      "cHJvZmlsZQ==",
		"APPLE_PROVISIONING_PROFILES":     "cHJvZmlsZXM=",
		"APPLE_TEAM_ID":                   "TEAM123456",
		"ASC_API_KEY_P8":                  "key",
		"ASC_KEY_ID":                      "KEY1234567",
		"ASC_ISSUER_ID":                   "00000000-0000-0000-0000-000000000000",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	credentials, err := takeAppleCredentials()
	if err != nil || credentials.teamID != "TEAM123456" || credentials.p12Password != " password " {
		t.Fatalf("takeAppleCredentials() = %#v, %v", credentials, err)
	}
	for name := range values {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("%s remained in environment", name)
		}
	}
}

func TestTakeAppleCredentialsAcceptsProfileBundleWithoutLegacyProfile(t *testing.T) {
	values := map[string]string{
		"APPLE_SIGNING_AGE_IDENTITY":      "AGE-SECRET-KEY-TEST",
		"APPLE_DISTRIBUTION_P12":          "cDEy",
		"APPLE_DISTRIBUTION_P12_PASSWORD": "password",
		"APPLE_PROVISIONING_PROFILE":      "",
		"APPLE_PROVISIONING_PROFILES":     "cHJvZmlsZXM=",
		"APPLE_TEAM_ID":                   "TEAM123456",
		"ASC_API_KEY_P8":                  "key",
		"ASC_KEY_ID":                      "KEY1234567",
		"ASC_ISSUER_ID":                   "",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	credentials, err := takeAppleCredentials()
	if err != nil || credentials.profile != "" || credentials.profiles == "" {
		t.Fatalf("takeAppleCredentials() = %#v, %v", credentials, err)
	}
}

func TestTakeAppleCredentialsAcceptsIssuerIDWithoutAnyStoredProfile(t *testing.T) {
	values := map[string]string{
		"APPLE_SIGNING_AGE_IDENTITY":      "AGE-SECRET-KEY-TEST",
		"APPLE_DISTRIBUTION_P12":          "cDEy",
		"APPLE_DISTRIBUTION_P12_PASSWORD": "password",
		"APPLE_PROVISIONING_PROFILE":      "",
		"APPLE_PROVISIONING_PROFILES":     "",
		"APPLE_TEAM_ID":                   "TEAM123456",
		"ASC_API_KEY_P8":                  "key",
		"ASC_KEY_ID":                      "KEY1234567",
		"ASC_ISSUER_ID":                   "00000000-0000-0000-0000-000000000000",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	// A new bundle identifier has no provisioning profile stored anywhere yet;
	// the App Store Connect issuer ID alone must be enough to attempt
	// discovering or creating one instead of failing before the deploy tries.
	credentials, err := takeAppleCredentials()
	if err != nil || credentials.profile != "" || credentials.profiles != "" || credentials.issuerID == "" {
		t.Fatalf("takeAppleCredentials() = %#v, %v", credentials, err)
	}
}

func TestTakeAppleCredentialsRejectsNoProfileAndNoIssuer(t *testing.T) {
	values := map[string]string{
		"APPLE_SIGNING_AGE_IDENTITY":      "AGE-SECRET-KEY-TEST",
		"APPLE_DISTRIBUTION_P12":          "cDEy",
		"APPLE_DISTRIBUTION_P12_PASSWORD": "password",
		"APPLE_PROVISIONING_PROFILE":      "",
		"APPLE_PROVISIONING_PROFILES":     "",
		"APPLE_TEAM_ID":                   "TEAM123456",
		"ASC_API_KEY_P8":                  "key",
		"ASC_KEY_ID":                      "KEY1234567",
		"ASC_ISSUER_ID":                   "",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	if _, err := takeAppleCredentials(); err == nil {
		t.Fatal("takeAppleCredentials() accepted no profile source and no issuer ID")
	}
}

func TestMaterializeProvisioningProfilesSupportsLegacyAndBundle(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, contents := range map[string]string{
		"TripNearby.mobileprovision":     "trip profile",
		"GravityKingdom.mobileprovision": "gravity profile",
	} {
		member, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	paths, err := materializeProvisioningProfiles(
		base64.StdEncoding.EncodeToString([]byte("legacy profile")),
		base64.StdEncoding.EncodeToString(archive.Bytes()),
		destination,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("profile paths = %#v", paths)
	}
	for _, profilePath := range paths {
		info, err := os.Stat(profilePath)
		if err != nil {
			t.Fatalf("stat profile %q: %v", profilePath, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("profile %q mode = %v", profilePath, info.Mode())
		}
	}
}

func TestMaterializeProvisioningProfilesRejectsUnsafeBundleMember(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	member, err := writer.Create("profiles/App.mobileprovision")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte("profile")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = materializeProvisioningProfiles("", base64.StdEncoding.EncodeToString(archive.Bytes()), t.TempDir())
	if err == nil {
		t.Fatal("nested profile bundle member was accepted")
	}
}

func TestSelectProvisioningProfileUsesExactBundleAndNewestUsableProfile(t *testing.T) {
	certificate := []byte("certificate DER")
	fingerprint := sha1.Sum(certificate) // #nosec G401 -- mirrors Apple's certificate fingerprint.
	identity := strings.ToUpper(hex.EncodeToString(fingerprint[:]))
	profile := func(name, bundleID string, expires time.Time, certificate []byte) provisioningProfileCandidate {
		return provisioningProfileCandidate{
			path: name + ".mobileprovision",
			profile: provisioningProfile{
				UUID:                  name,
				Name:                  name,
				ExpirationDate:        expires,
				Platform:              []string{"iOS"},
				TeamIdentifier:        []string{"TEAM123456"},
				DeveloperCertificates: [][]byte{certificate},
				Entitlements: map[string]any{
					"application-identifier": "TEAM123456." + bundleID,
					"get-task-allow":         false,
				},
			},
		}
	}
	now := time.Now()
	candidates := []provisioningProfileCandidate{
		profile("other-app", "com.example.other", now.Add(72*time.Hour), certificate),
		profile("expired", "com.example.app", now.Add(-time.Hour), certificate),
		profile("wrong-certificate", "com.example.app", now.Add(96*time.Hour), []byte("other")),
		profile("selected", "com.example.app", now.Add(48*time.Hour), certificate),
	}
	selected, err := selectProvisioningProfile(candidates, "TEAM123456", "com.example.app", identity)
	if err != nil {
		t.Fatal(err)
	}
	if selected.profile.Name != "selected" {
		t.Fatalf("selected profile = %q", selected.profile.Name)
	}
	if _, err := selectProvisioningProfile(candidates, "TEAM123456", "com.example.missing", identity); err == nil {
		t.Fatal("missing Bundle ID unexpectedly selected a profile")
	}
}

func TestExecuteTestFlightEncryptsCredentialFailure(t *testing.T) {
	for _, name := range []string{
		"APPLE_SIGNING_AGE_IDENTITY", "APPLE_DISTRIBUTION_P12", "APPLE_DISTRIBUTION_P12_PASSWORD",
		"APPLE_PROVISIONING_PROFILE", "APPLE_TEAM_ID", "ASC_API_KEY_P8", "ASC_KEY_ID", "ASC_ISSUER_ID",
	} {
		t.Setenv(name, "")
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	options := &TestFlightOptions{
		EncryptedIPAPath: filepath.Join(root, "intermediate", "App.ipa.age"),
		LogPath:          filepath.Join(root, "private-output", "build.log"),
		BuildNumber:      "42.1",
	}
	output := filepath.Join(root, "encrypted")
	err = ExecuteTestFlight(t.Context(), options, identity.Recipient().String(), output)
	if !errors.Is(err, ErrDeployFailed) {
		t.Fatalf("ExecuteTestFlight() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "build.log.age")); err != nil {
		t.Fatalf("encrypted diagnostic missing: %v", err)
	}
	if _, err := os.Stat(options.LogPath); !os.IsNotExist(err) {
		t.Fatalf("plaintext diagnostic remained: %v", err)
	}
}

type zipEntry struct {
	data string
	mode os.FileMode
}

func writeDeployTestZIP(t *testing.T, entries map[string]zipEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "App.ipa")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, entry := range entries {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(entry.mode)
		member, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write([]byte(entry.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWriteTextOrBase64Secret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "key.p8")
	if err := writeTextOrBase64Secret("-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----", path, "PRIVATE KEY"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "BEGIN PRIVATE KEY") {
		t.Fatalf("key = %q, %v", data, err)
	}
}

func TestSetBundleBuildNumber(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), "Info.plist")
	original, err := plist.Marshal(map[string]any{
		"CFBundleIdentifier": "com.example.app",
		"CFBundleVersion":    "7",
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	if err := setBundleBuildNumber(infoPath, "108.2"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if _, err := plist.Unmarshal(updated, &values); err != nil {
		t.Fatal(err)
	}
	if values["CFBundleVersion"] != "108.2" || values["CFBundleIdentifier"] != "com.example.app" {
		t.Fatalf("updated Info.plist = %#v", values)
	}
}

func TestSetExportComplianceIfUnsetDeclaresNoNonExemptEncryption(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), "Info.plist")
	original, err := plist.Marshal(map[string]any{
		"CFBundleIdentifier": "com.example.app",
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	if err := setExportComplianceIfUnset(infoPath); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if _, err := plist.Unmarshal(updated, &values); err != nil {
		t.Fatal(err)
	}
	if declared, ok := values["ITSAppUsesNonExemptEncryption"].(bool); !ok || declared {
		t.Fatalf("ITSAppUsesNonExemptEncryption = %#v, want false", values["ITSAppUsesNonExemptEncryption"])
	}
}

func TestSetExportComplianceIfUnsetRespectsExistingDeclaration(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), "Info.plist")
	original, err := plist.Marshal(map[string]any{
		"CFBundleIdentifier":            "com.example.app",
		"ITSAppUsesNonExemptEncryption": true,
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	// A project that genuinely ships non-exempt cryptography must still
	// answer Apple's compliance questionnaire itself; this must never
	// silently flip an explicit true declaration to false.
	if err := setExportComplianceIfUnset(infoPath); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if _, err := plist.Unmarshal(updated, &values); err != nil {
		t.Fatal(err)
	}
	if declared, ok := values["ITSAppUsesNonExemptEncryption"].(bool); !ok || !declared {
		t.Fatalf("ITSAppUsesNonExemptEncryption = %#v, want unchanged true", values["ITSAppUsesNonExemptEncryption"])
	}
}

func TestTestFlightBuildNumberValidation(t *testing.T) {
	root := t.TempDir()
	for _, number := range []string{"", "0", "01", "1.", "1.a", "1.2.3.4"} {
		options := &TestFlightOptions{
			EncryptedIPAPath: filepath.Join(root, "intermediate", "App.ipa.age"),
			LogPath:          filepath.Join(root, "private-output", "build.log"),
			BuildNumber:      number,
		}
		if err := options.validate(); err == nil {
			t.Fatalf("build number %q was accepted", number)
		}
	}
	for _, number := range []string{"1", "42.1", "9999.99.99"} {
		options := &TestFlightOptions{
			EncryptedIPAPath: filepath.Join(root, "intermediate", "App.ipa.age"),
			LogPath:          filepath.Join(root, "private-output", "build.log"),
			BuildNumber:      number,
		}
		if err := options.validate(); err != nil {
			t.Fatalf("build number %q rejected: %v", number, err)
		}
	}
}

func discoveredNames(t *testing.T, app string) []string {
	t.Helper()
	bundles, err := discoverEmbeddedBundles(app)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(bundles))
	for _, bundle := range bundles {
		names = append(names, filepath.Base(bundle.path))
	}
	return names
}

func TestDiscoverEmbeddedBundlesFindsPlugInsExtensions(t *testing.T) {
	app := filepath.Join(t.TempDir(), "App.app")
	for _, relative := range []string{
		"PlugIns/Widget.appex/Assets", "PlugIns/Share.appex", "Frameworks/Kit.framework",
	} {
		if err := os.MkdirAll(filepath.Join(app, relative), 0700); err != nil {
			t.Fatal(err)
		}
	}
	names := discoveredNames(t, app)
	sort.Strings(names)
	if strings.Join(names, ",") != "Share.appex,Widget.appex" {
		t.Fatalf("extensions = %#v", names)
	}

	plain := filepath.Join(t.TempDir(), "Plain.app")
	if err := os.MkdirAll(filepath.Join(plain, "Frameworks"), 0700); err != nil {
		t.Fatal(err)
	}
	if bundles, err := discoverEmbeddedBundles(plain); err != nil || len(bundles) != 0 {
		t.Fatalf("plain application = %#v, %v", bundles, err)
	}
}

func TestDiscoverEmbeddedBundlesSignsInsideOut(t *testing.T) {
	app := filepath.Join(t.TempDir(), "App.app")
	for _, relative := range []string{
		"PlugIns/Widget.appex",
		"Watch/Watch.app/Frameworks/Kit.framework",
		"Watch/Watch.app/PlugIns/Complication.appex/Assets",
		"Watch/Watch.app/PlugIns/Another.appex",
	} {
		if err := os.MkdirAll(filepath.Join(app, relative), 0700); err != nil {
			t.Fatal(err)
		}
	}
	bundles, err := discoverEmbeddedBundles(app)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, bundle := range bundles {
		order = append(order, filepath.Base(bundle.path))
	}
	// The application's extensions, then the watch application's extensions,
	// then the watch application: every bundle precedes its container.
	if want := "Widget.appex,Another.appex,Complication.appex,Watch.app"; strings.Join(order, ",") != want {
		t.Fatalf("order = %v, want %s", order, want)
	}
	for _, bundle := range bundles {
		switch filepath.Base(bundle.path) {
		case "Watch.app":
			if bundle.kind != embeddedWatchApp || bundle.parent != app {
				t.Fatalf("watch application = %#v", bundle)
			}
		case "Widget.appex":
			if bundle.kind != embeddedExtension || bundle.parent != app {
				t.Fatalf("widget = %#v", bundle)
			}
		default:
			if bundle.kind != embeddedExtension || bundle.parent != filepath.Join(app, "Watch", "Watch.app") {
				t.Fatalf("watch extension = %#v", bundle)
			}
		}
	}
}

func TestDiscoverEmbeddedBundlesStillFailsClosedOnEverythingElse(t *testing.T) {
	for _, relative := range []string{
		"AppClips", "XPCServices/Service.xpc", "Nested.App",
		"Extensions/Kit.appex",                        // ExtensionKit, not PlugIns
		"Frameworks/Share.appex",                      // an extension outside PlugIns
		"PlugIns/Widget.appex/PlugIns/Inner.appex",    // nested inside an extension
		"PlugIns/Widget.appex/Watch",                  // same, another bundle type
		"PlugIns/Widget.appex/Watch/Watch.app",        // a watch application inside an extension
		"PlugIns/NotAnExtension",                      // PlugIns holds extensions only
		"Resources/PlugIns",                           // a second PlugIns elsewhere
		"Resources/Watch",                             // a second Watch elsewhere
		"watch",                                       // wrong case is not the Watch folder
		"Watch/Other.appex",                           // Watch holds watch applications only
		"Watch/NotAnApp",                              // same
		"Watch/A.app/Watch",                           // a watch application inside a watch application
		"Watch/A.app/Watch/B.app",                     // same, with a bundle
		"Watch/A.app/AppClips",                        // App Clip in a watch application
		"Watch/A.app/XPCServices/S.xpc",               // XPC service in a watch application
		"Watch/A.app/Extensions/Kit.appex",            // ExtensionKit in a watch application
		"Watch/A.app/PlugIns/NotAnExtension",          // watch PlugIns holds extensions only
		"Watch/A.app/PlugIns/W.appex/PlugIns/I.appex", // nested inside a watch extension
		"Watch/A.app/PlugIns/W.appex/Watch/B.app",     // same, another bundle type
		"Watch/A.app/Frameworks/F.framework/B.app",    // an application hidden in a framework
		"Watch/A.app/Frameworks/Share.appex",          // an extension outside PlugIns
	} {
		t.Run(relative, func(t *testing.T) {
			app := filepath.Join(t.TempDir(), "App.app")
			if err := os.MkdirAll(filepath.Join(app, relative), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := discoverEmbeddedBundles(app); err == nil {
				t.Fatalf("nested bundle %q was accepted", relative)
			}
		})
	}
}

func TestDiscoverEmbeddedBundlesIsBounded(t *testing.T) {
	app := filepath.Join(t.TempDir(), "App.app")
	for index := 0; index <= maxAppExtensions; index++ {
		if err := os.MkdirAll(filepath.Join(app, "PlugIns", fmt.Sprintf("E%02d.appex", index)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := discoverEmbeddedBundles(app); err == nil {
		t.Fatal("more extensions than the limit were accepted")
	}

	watch := filepath.Join(t.TempDir(), "App.app")
	for index := 0; index <= maxAppExtensions; index++ {
		if err := os.MkdirAll(filepath.Join(watch, "Watch", "W.app", "PlugIns", fmt.Sprintf("E%02d.appex", index)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := discoverEmbeddedBundles(watch); err == nil {
		t.Fatal("more watch extensions than the limit were accepted")
	}

	two := filepath.Join(t.TempDir(), "App.app")
	for _, name := range []string{"A.app", "B.app"} {
		if err := os.MkdirAll(filepath.Join(two, "Watch", name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := discoverEmbeddedBundles(two); err == nil {
		t.Fatal("two watch applications were accepted")
	}
}

func writeBundleInfo(t *testing.T, bundlePath string, values map[string]any) {
	t.Helper()
	info, err := plist.Marshal(values, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bundlePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundlePath, "Info.plist"), info, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReadEmbeddedBundleIDMustExtendTheContainer(t *testing.T) {
	write := func(bundleID string) string {
		t.Helper()
		bundle := filepath.Join(t.TempDir(), "Widget.appex")
		writeBundleInfo(t, bundle, map[string]any{"CFBundleIdentifier": bundleID})
		return bundle
	}
	for _, kind := range []embeddedKind{embeddedExtension, embeddedWatchApp} {
		for _, accepted := range []string{"com.example.app.widget", "com.example.app.Live-Activity", "com.example.app.watchkitapp"} {
			if got, err := readEmbeddedBundleID(write(accepted), "com.example.app", kind); err != nil || got != accepted {
				t.Fatalf("%q = %q, %v", accepted, got, err)
			}
		}
		for _, rejected := range []string{
			"com.example.app",       // the container itself
			"com.example.appwidget", // shares a prefix, is not a child
			"com.example.other",     // unrelated identifier
			"com.example.app.",      // empty child
			"com.example.app..x",    // empty component
			"com.example.app.x y",   // outside the identifier alphabet
		} {
			if _, err := readEmbeddedBundleID(write(rejected), "com.example.app", kind); err == nil {
				t.Fatalf("%s identifier %q was accepted", kind.label(), rejected)
			}
		}
	}
}

// writeWatchLayout builds an application with a widget, a watch application
// and an extension inside the watch application.
func writeWatchLayout(t *testing.T, watchID, companion, watchExtensionID string) (string, string) {
	t.Helper()
	app := filepath.Join(t.TempDir(), "App.app")
	writeBundleInfo(t, app, map[string]any{"CFBundleIdentifier": "com.example.app", "CFBundleShortVersionString": "1.4.0", "CFBundleVersion": "1"})
	writeBundleInfo(t, filepath.Join(app, "PlugIns", "Widget.appex"), map[string]any{"CFBundleIdentifier": "com.example.app.widget"})
	watch := filepath.Join(app, "Watch", "Watch.app")
	writeBundleInfo(t, watch, map[string]any{
		"CFBundleIdentifier": watchID, "WKCompanionAppBundleIdentifier": companion,
		"CFBundleShortVersionString": "0.9", "CFBundleVersion": "1",
	})
	if watchExtensionID != "" {
		writeBundleInfo(t, filepath.Join(watch, "PlugIns", "Complication.appex"), map[string]any{"CFBundleIdentifier": watchExtensionID})
	}
	return app, watch
}

func TestValidateEmbeddedBundlesAcceptsWatchApplication(t *testing.T) {
	app, _ := writeWatchLayout(t, "com.example.app.watchkitapp", "com.example.app", "com.example.app.watchkitapp.complication")
	bundles, err := discoverEmbeddedBundles(app)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := validateEmbeddedBundles(bundles, app, "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids, ","); got != "com.example.app.widget,com.example.app.watchkitapp.complication,com.example.app.watchkitapp" {
		t.Fatalf("identifiers = %s", got)
	}
}

func TestValidateEmbeddedBundlesRejectsBadWatchApplication(t *testing.T) {
	for name, layout := range map[string][3]string{
		"watch identifier does not extend the application": {"com.other.watchkitapp", "com.example.app", ""},
		"watch identifier equals the application":          {"com.example.app", "com.example.app", ""},
		"companion is another application":                 {"com.example.app.watchkitapp", "com.other.app", ""},
		"companion is missing":                             {"com.example.app.watchkitapp", "", ""},
		"watch extension does not extend the watch app":    {"com.example.app.watchkitapp", "com.example.app", "com.example.app.complication"},
		"watch extension reuses the widget identifier":     {"com.example.app.watchkitapp", "com.example.app", "com.example.app.widget"},
	} {
		t.Run(name, func(t *testing.T) {
			app, _ := writeWatchLayout(t, layout[0], layout[1], layout[2])
			bundles, err := discoverEmbeddedBundles(app)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := validateEmbeddedBundles(bundles, app, "com.example.app"); err == nil {
				t.Fatal("invalid watch application was accepted")
			}
		})
	}
}

func TestValidateEmbeddedBundlesClearMessages(t *testing.T) {
	app, _ := writeWatchLayout(t, "com.other.watchkitapp", "com.example.app", "")
	bundles, _ := discoverEmbeddedBundles(app)
	if _, err := validateEmbeddedBundles(bundles, app, "com.example.app"); err == nil || !strings.Contains(err.Error(), "watch application bundle identifier must extend") {
		t.Fatalf("error = %v", err)
	}
	app, _ = writeWatchLayout(t, "com.example.app.watchkitapp", "com.other.app", "")
	bundles, _ = discoverEmbeddedBundles(app)
	if _, err := validateEmbeddedBundles(bundles, app, "com.example.app"); err == nil || !strings.Contains(err.Error(), "WKCompanionAppBundleIdentifier") {
		t.Fatalf("error = %v", err)
	}
}

func TestBundleShortVersionRoundTrip(t *testing.T) {
	app, watch := writeWatchLayout(t, "com.example.app.watchkitapp", "com.example.app", "")
	version, err := readBundleShortVersion(filepath.Join(app, "Info.plist"))
	if err != nil || version != "1.4.0" {
		t.Fatalf("version = %q, %v", version, err)
	}
	infoPath := filepath.Join(watch, "Info.plist")
	if err := setBundleShortVersion(infoPath, version); err != nil {
		t.Fatal(err)
	}
	if err := setBundleBuildNumber(infoPath, "42.1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if _, err := plist.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	if values["CFBundleShortVersionString"] != "1.4.0" || values["CFBundleVersion"] != "42.1" ||
		values["WKCompanionAppBundleIdentifier"] != "com.example.app" {
		t.Fatalf("watch Info.plist = %#v", values)
	}
	// An application with no marketing version has nothing to sync.
	bare := filepath.Join(t.TempDir(), "Bare.app")
	writeBundleInfo(t, bare, map[string]any{"CFBundleIdentifier": "com.example.bare"})
	if version, err := readBundleShortVersion(filepath.Join(bare, "Info.plist")); err != nil || version != "" {
		t.Fatalf("bare version = %q, %v", version, err)
	}
}

func TestIsInsideWatchApp(t *testing.T) {
	app, _ := writeWatchLayout(t, "com.example.app.watchkitapp", "com.example.app", "com.example.app.watchkitapp.complication")
	bundles, err := discoverEmbeddedBundles(app)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, bundle := range bundles {
		got[filepath.Base(bundle.path)] = isInsideWatchApp(bundles, bundle)
	}
	want := map[string]bool{"Widget.appex": false, "Complication.appex": true, "Watch.app": true}
	for name, expected := range want {
		if got[name] != expected {
			t.Fatalf("%s inside watch app = %v, want %v", name, got[name], expected)
		}
	}
}

func TestNestedCodeLeavesExtensionsToTheirOwnSigning(t *testing.T) {
	app := filepath.Join(t.TempDir(), "App.app")
	for _, relative := range []string{
		"Frameworks/Kit.framework/Versions", "PlugIns/Widget.appex/Frameworks/Inner.framework",
		"Watch/Watch.app/Frameworks/WatchKit2.framework",
	} {
		if err := os.MkdirAll(filepath.Join(app, relative), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(app, "Frameworks", "libswift.dylib"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	applicationCode, err := nestedCode(app, filepath.Join(app, "PlugIns"), filepath.Join(app, "Watch"))
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range applicationCode {
		if strings.Contains(code, "PlugIns") || strings.Contains(code, "Watch") {
			t.Fatalf("application re-signed extension code %q", code)
		}
	}
	if len(applicationCode) != 2 {
		t.Fatalf("application nested code = %#v", applicationCode)
	}

	extensionCode, err := nestedCode(filepath.Join(app, "PlugIns", "Widget.appex"))
	if err != nil {
		t.Fatal(err)
	}
	if len(extensionCode) != 1 || filepath.Base(extensionCode[0]) != "Inner.framework" {
		t.Fatalf("extension nested code = %#v", extensionCode)
	}

	watchCode, err := nestedCode(filepath.Join(app, "Watch", "Watch.app"), filepath.Join(app, "Watch", "Watch.app", "PlugIns"))
	if err != nil {
		t.Fatal(err)
	}
	if len(watchCode) != 1 || filepath.Base(watchCode[0]) != "WatchKit2.framework" {
		t.Fatalf("watch nested code = %#v", watchCode)
	}
}

func TestAltoolArgsSupportsTeamAndIndividualKeys(t *testing.T) {
	team := altoolArgs("--validate-app", "/tmp/App.ipa", &appleCredentials{apiKeyID: "KEY1234567", issuerID: "issuer"})
	if !strings.Contains(strings.Join(team, " "), "--apiIssuer issuer") {
		t.Fatalf("team args = %#v", team)
	}
	individual := altoolArgs("--upload-app", "/tmp/App.ipa", &appleCredentials{apiKeyID: "KEY1234567"})
	if strings.Contains(strings.Join(individual, " "), "--apiIssuer") {
		t.Fatalf("individual args = %#v", individual)
	}
}

func TestASCBundleIDNameUsesOnlyLettersDigitsAndSpaces(t *testing.T) {
	cases := map[string]string{
		"com.KeyoLock.app.watchkitapp": "com KeyoLock app watchkitapp",
		"com.example.app":              "com example app",
		"com.example-app.ext_1":        "com example app ext 1",
	}
	for in, want := range cases {
		if got := ascBundleIDName(in); got != want {
			t.Errorf("ascBundleIDName(%q) = %q, want %q", in, got, want)
		}
	}
}
