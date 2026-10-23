package runner

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" // #nosec G505 -- mirrors Apple's signing certificate fingerprint.
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"howett.net/plist"
	"software.sslmate.com/src/go-pkcs12"
)

func validMacOSInputs(t *testing.T) Inputs {
	t.Helper()
	in := validInputs(t)
	in.Operation = OperationTestFlight
	in.Platform = PlatformMacOS
	in.TestScript = "scripts/build-app-store.sh"
	in.ArtifactPath = "dist/App.app"
	return in
}

func TestInputsValidateMacOSPlatform(t *testing.T) {
	valid := validMacOSInputs(t)
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid macOS deployment rejected: %v", err)
	}
	for _, platform := range []string{"", PlatformIOS} {
		in := validInputs(t)
		in.Platform = platform
		if err := in.Validate(); err != nil {
			t.Errorf("platform %q rejected: %v", platform, err)
		}
	}
	tests := []struct {
		name   string
		mutate func(*Inputs)
	}{
		{"unknown platform", func(in *Inputs) { in.Platform = "tvos" }},
		{"macOS build operation", func(in *Inputs) { in.Operation = OperationBuild }},
		{"macOS test operation", func(in *Inputs) { in.Operation = OperationTest }},
		{"no build script", func(in *Inputs) { in.TestScript = "" }},
		{"traversing build script", func(in *Inputs) { in.TestScript = "../build.sh" }},
		{"no application", func(in *Inputs) { in.ArtifactPath = "" }},
		{"not an application", func(in *Inputs) { in.ArtifactPath = "dist/App.zip" }},
		{"bare extension", func(in *Inputs) { in.ArtifactPath = "dist/.app" }},
		{"application in Git metadata", func(in *Inputs) { in.ArtifactPath = ".git/App.app" }},
		{"iOS testflight with script", func(in *Inputs) { in.Platform = PlatformIOS }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := validMacOSInputs(t)
			test.mutate(&in)
			if err := in.Validate(); err == nil {
				t.Fatal("invalid inputs accepted")
			}
		})
	}
}

func writeMacOSApp(t *testing.T, root string) string {
	t.Helper()
	app := filepath.Join(root, "dist", "App.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0755); err != nil {
		t.Fatal(err)
	}
	info, err := plist.Marshal(map[string]any{"CFBundleIdentifier": "com.example.mac", "CFBundleExecutable": "App"}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), info, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "App"), []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestResolveMacOSApp(t *testing.T) {
	root := t.TempDir()
	app := writeMacOSApp(t, root)
	resolved, err := ResolveMacOSApp(root, "dist/App.app")
	if err != nil {
		t.Fatalf("resolve application: %v", err)
	}
	if wantRoot, _ := filepath.EvalSymlinks(root); resolved != filepath.Join(wantRoot, "dist", "App.app") {
		t.Errorf("resolved %s", resolved)
	}
	if _, err := ResolveMacOSApp(root, "dist/Missing.app"); err == nil {
		t.Error("missing application accepted")
	}
	if err := os.Symlink("MacOS", filepath.Join(app, "Contents", "Current")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveMacOSApp(root, "dist/App.app"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("bundle with a symbolic link: %v", err)
	}

	outside := t.TempDir()
	writeMacOSApp(t, outside)
	escaping := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "dist"), filepath.Join(escaping, "dist")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveMacOSApp(escaping, "dist/App.app"); err == nil {
		t.Error("application outside the snapshot accepted")
	}
}

func TestExecuteMacOSBuildSecurePackagesTheApplication(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ditto"); err != nil {
		t.Skip("packaging uses ditto, which only macOS has")
	}
	work := t.TempDir()
	source := filepath.Join(work, "source")
	if err := os.MkdirAll(filepath.Join(source, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, ".git", "config"), "[core]\n\tbare = false\n")
	script := "mkdir -p dist/App.app/Contents/MacOS\n" +
		"printf '<plist><dict><key>CFBundleIdentifier</key><string>com.example.mac</string></dict></plist>' > dist/App.app/Contents/Info.plist\n" +
		"echo binary > dist/App.app/Contents/MacOS/App\n"
	if err := os.WriteFile(filepath.Join(source, "scripts", "build.sh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	options := &MacOSBuildOptions{
		SourceRoot: source, Script: "scripts/build.sh", App: "dist/App.app",
		LogPath: filepath.Join(work, "private-output", "build.log"),
		IPAPath: filepath.Join(work, "private-output", "App.ipa"), Timeout: time.Minute,
	}
	recipient := identity.Recipient().String()
	if err := ExecuteMacOSBuildSecure(context.Background(), options, recipient, recipient, filepath.Join(work, "encrypted")); err != nil {
		t.Fatalf("macOS build: %v", err)
	}
	for _, name := range []string{"build.log.age", "App.ipa.age"} {
		if _, err := os.Stat(filepath.Join(work, "encrypted", name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}

func TestExecuteMacOSBuildSecureFailureUploadsOnlyTheLog(t *testing.T) {
	work := t.TempDir()
	source := filepath.Join(work, "source")
	if err := os.MkdirAll(filepath.Join(source, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, ".git", "config"), "[core]\n\tbare = false\n")
	if err := os.WriteFile(filepath.Join(source, "scripts", "build.sh"), []byte("echo private failure detail\nexit 3\n"), 0755); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	options := &MacOSBuildOptions{
		SourceRoot: source, Script: "scripts/build.sh", App: "dist/App.app",
		LogPath: filepath.Join(work, "private-output", "build.log"),
		IPAPath: filepath.Join(work, "private-output", "App.ipa"), Timeout: time.Minute,
	}
	recipient := identity.Recipient().String()
	if err := ExecuteMacOSBuildSecure(context.Background(), options, recipient, recipient, filepath.Join(work, "encrypted")); err != ErrBuildFailed {
		t.Fatalf("failed build returned %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(work, "encrypted"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "build.log.age" {
		t.Fatalf("encrypted output = %v (%v), want only build.log.age", entries, err)
	}
	ciphertext, err := os.Open(filepath.Join(work, "encrypted", "build.log.age"))
	if err != nil {
		t.Fatal(err)
	}
	defer ciphertext.Close()
	reader, err := age.Decrypt(ciphertext, identity)
	if err != nil {
		t.Fatal(err)
	}
	log, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(log), "private failure detail") || !strings.Contains(string(log), "Build failed") {
		t.Errorf("log does not record the failure:\n%s", log)
	}
	if _, err := os.Stat(options.LogPath); !os.IsNotExist(err) {
		t.Error("plaintext log was left behind")
	}
}

func TestMacOSBuildOptionsRequireFixedPrivateOutput(t *testing.T) {
	work := t.TempDir()
	valid := MacOSBuildOptions{
		SourceRoot: filepath.Join(work, "source"), Script: "build.sh", App: "App.app",
		LogPath: filepath.Join(work, "private-output", "build.log"),
		IPAPath: filepath.Join(work, "private-output", "App.ipa"), Timeout: time.Minute,
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	for name, mutate := range map[string]func(*MacOSBuildOptions){
		"long timeout":    func(o *MacOSBuildOptions) { o.Timeout = 2 * time.Hour },
		"no timeout":      func(o *MacOSBuildOptions) { o.Timeout = 0 },
		"log name":        func(o *MacOSBuildOptions) { o.LogPath = filepath.Join(work, "private-output", "other.log") },
		"log in checkout": func(o *MacOSBuildOptions) { o.SourceRoot = work },
		"not an app":      func(o *MacOSBuildOptions) { o.App = "App.zip" },
		"relative source": func(o *MacOSBuildOptions) { o.SourceRoot = "source" },
	} {
		options := valid
		mutate(&options)
		if err := options.validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCheckMacOSBundleLayout(t *testing.T) {
	root := t.TempDir()
	app := writeMacOSApp(t, root)
	if err := os.MkdirAll(filepath.Join(app, "Contents", "Frameworks", "Kit.framework"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(app, "Contents", "Resources", "App_App.bundle"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := checkMacOSBundleLayout(app); err != nil {
		t.Fatalf("plain application rejected: %v", err)
	}
	for _, nested := range []string{"Contents/Library/LoginItems/Helper.app", "Contents/PlugIns/Widget.appex", "Contents/XPCServices/Worker.xpc"} {
		path := filepath.Join(app, filepath.FromSlash(nested))
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := checkMacOSBundleLayout(app); err == nil {
			t.Errorf("nested %s accepted", nested)
		}
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	iosApp := filepath.Join(root, "Phone.app")
	if err := os.MkdirAll(iosApp, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iosApp, "Info.plist"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkMacOSBundleLayout(iosApp); err == nil {
		t.Error("iOS application accepted as a macOS application")
	}
}

func TestMacSandboxEntitlements(t *testing.T) {
	declared, err := plist.Marshal(map[string]any{
		"com.apple.security.app-sandbox":                      true,
		"com.apple.security.network.client":                   true,
		"com.apple.security.temporary-exception.apple-events": []string{"com.apple.Music"},
		"com.apple.developer.team-identifier":                 "OTHERTEAM1",
		"get-task-allow":                                      true,
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := macSandboxEntitlements(declared)
	if err != nil {
		t.Fatalf("sandboxed entitlements rejected: %v", err)
	}
	if len(sandbox) != 3 || sandbox["com.apple.developer.team-identifier"] != nil || sandbox["get-task-allow"] != nil {
		t.Errorf("kept %v, want only the com.apple.security entries", sandbox)
	}
	unsandboxed, _ := plist.Marshal(map[string]any{"com.apple.security.network.client": true}, plist.XMLFormat)
	if _, err := macSandboxEntitlements(unsandboxed); err == nil {
		t.Error("application without the App Sandbox accepted")
	}
	if _, err := macSandboxEntitlements(nil); err == nil {
		t.Error("application without entitlements accepted")
	}

	profile := &provisioningProfile{Entitlements: map[string]any{
		"com.apple.application-identifier":    "TEAM123456.com.example.mac",
		"com.apple.developer.team-identifier": "TEAM123456",
		"keychain-access-groups":              []any{"TEAM123456.*"},
	}}
	signing, err := macSigningEntitlements(profile, "TEAM123456", sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if signing["com.apple.application-identifier"] != "TEAM123456.com.example.mac" ||
		signing["com.apple.developer.team-identifier"] != "TEAM123456" || signing["keychain-access-groups"] != nil ||
		signing["com.apple.security.app-sandbox"] != true || len(signing) != 5 {
		t.Errorf("signing entitlements = %v", signing)
	}
	if _, err := macSigningEntitlements(profile, "OTHERTEAM1", sandbox); err == nil {
		t.Error("profile of another team accepted")
	}
}

func TestSelectMacProvisioningProfile(t *testing.T) {
	certificate := []byte("distribution certificate")
	fingerprint := sha1Hex(certificate)
	profile := func(uuid, appID string, platform string, expires time.Time) provisioningProfileCandidate {
		return provisioningProfileCandidate{path: uuid, profile: provisioningProfile{
			UUID: uuid, Name: uuid, ExpirationDate: expires, Platform: []string{platform},
			TeamIdentifier: []string{"TEAM123456"}, DeveloperCertificates: [][]byte{certificate},
			Entitlements: map[string]any{"com.apple.application-identifier": appID},
		}}
	}
	later, sooner := time.Now().Add(48*time.Hour), time.Now().Add(24*time.Hour)
	candidates := []provisioningProfileCandidate{
		profile("ios", "TEAM123456.com.example.mac", "iOS", later.Add(time.Hour)),
		profile("other", "TEAM123456.com.example.other", "OSX", later),
		profile("old", "TEAM123456.com.example.mac", "OSX", sooner),
		profile("new", "TEAM123456.com.example.mac", "OSX", later),
	}
	selected, err := selectMacProvisioningProfile(candidates, "TEAM123456", "com.example.mac", fingerprint)
	if err != nil || selected.profile.UUID != "new" {
		t.Fatalf("selected %q, %v; want the newest macOS profile", selected.profile.UUID, err)
	}
	if _, err := selectMacProvisioningProfile(candidates[:2], "TEAM123456", "com.example.mac", fingerprint); err == nil {
		t.Error("an iOS profile was accepted for a macOS application")
	}
	if _, err := selectMacProvisioningProfile(candidates, "TEAM123456", "com.example.mac", strings.Repeat("0", 40)); err == nil {
		t.Error("a profile for another certificate was accepted")
	}
}

func TestFindSigningIdentity(t *testing.T) {
	output := `  1) 1111111111111111111111111111111111111111 "Apple Distribution: Example (TEAM123456)"
  2) 2222222222222222222222222222222222222222 "3rd Party Mac Developer Installer: Other (OTHERTEAM1)"
  3) 3333333333333333333333333333333333333333 "3rd Party Mac Developer Installer: Example (TEAM123456)"
     3 valid identities found`
	fingerprint, name, ok := findSigningIdentity(output, "TEAM123456", macInstallerPrefixes...)
	if !ok || fingerprint != strings.Repeat("3", 40) || name != "3rd Party Mac Developer Installer: Example (TEAM123456)" {
		t.Errorf("installer identity = %q %q %v", fingerprint, name, ok)
	}
	if _, name, ok := findSigningIdentity(output, "TEAM123456", appleDistributionPrefix); !ok || !strings.HasPrefix(name, "Apple Distribution") {
		t.Errorf("distribution identity = %q %v", name, ok)
	}
	if _, _, ok := findSigningIdentity(output, "NOTEAM0000", macInstallerPrefixes...); ok {
		t.Error("identity of another team accepted")
	}
}

func TestTestFlightOptionsPlatform(t *testing.T) {
	work := t.TempDir()
	options := TestFlightOptions{
		EncryptedIPAPath: filepath.Join(work, "App.ipa.age"), LogPath: filepath.Join(work, "build.log"), BuildNumber: "12.1",
	}
	for _, platform := range []string{"", PlatformIOS, PlatformMacOS} {
		options.Platform = platform
		if err := options.validate(); err != nil {
			t.Errorf("platform %q rejected: %v", platform, err)
		}
	}
	options.Platform = "watchos"
	if err := options.validate(); err == nil {
		t.Error("unknown platform accepted")
	}
}

func TestAltoolPlatformArgs(t *testing.T) {
	args := altoolPlatformArgs("--upload-app", "/tmp/App.pkg", "macos", &appleCredentials{apiKeyID: "KEY1234567", issuerID: "issuer"})
	if strings.Join(args, " ") != "altool --upload-app -f /tmp/App.pkg -t macos --apiKey KEY1234567 --apiIssuer issuer" {
		t.Errorf("altool arguments = %v", args)
	}
}

func TestMacProvisioningUsesMacProfilesAndExistingIdentifier(t *testing.T) {
	profile := base64.StdEncoding.EncodeToString([]byte("mac profile"))
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/bundleIds":
			// An App ID registered in the Developer Portal is listed as IOS.
			writeASCJSON(w, `{"data":[{"type":"bundleIds","id":"bundle1","attributes":{"identifier":"com.example.mac","platform":"IOS"}}]}`)
		case r.URL.Path == "/v1/bundleIds/bundle1/profiles":
			writeASCJSON(w, `{"data":[`+
				`{"type":"profiles","id":"p1","attributes":{"profileType":"IOS_APP_STORE","profileState":"ACTIVE","profileContent":"`+profile+`"}},`+
				`{"type":"profiles","id":"p2","attributes":{"profileType":"MAC_APP_STORE","profileState":"ACTIVE","profileContent":"`+profile+`"}}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/bundleIds":
			created = true
			http.Error(w, "conflict", http.StatusConflict)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client := testASCClient(t, server.Client())
	client.baseURL = server.URL
	resourceID, paths, err := downloadASCPlatformProfiles(context.Background(), client, macProvisioning, "com.example.mac", t.TempDir())
	if err != nil || resourceID != "bundle1" || created {
		t.Fatalf("resource %q, created %v, err %v", resourceID, created, err)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], ".provisionprofile") {
		t.Errorf("downloaded %v, want only the Mac App Store profile", paths)
	}
}

func TestCreateMacInstallerIdentity(t *testing.T) {
	var requestedType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/certificates" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Data struct {
				Attributes struct {
					CertificateType string `json:"certificateType"`
					CSRContent      string `json:"csrContent"`
				} `json:"attributes"`
			} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requestedType = body.Data.Attributes.CertificateType
		block, _ := pem.Decode([]byte(body.Data.Attributes.CSRContent))
		if block == nil {
			t.Fatal("certificate request is not PEM")
		}
		request, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || request.CheckSignature() != nil {
			t.Fatalf("invalid certificate request: %v", err)
		}
		issuerKey := mustRSAKey(t)
		template := &x509.Certificate{
			SerialNumber: big.NewInt(7), Subject: request.Subject,
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, request.PublicKey, issuerKey)
		if err != nil {
			t.Fatal(err)
		}
		writeASCJSON(w, `{"data":{"type":"certificates","id":"cert1","attributes":{"certificateType":"MAC_INSTALLER_DISTRIBUTION","certificateContent":"`+
			base64.StdEncoding.EncodeToString(der)+`"}}}`)
	}))
	defer server.Close()
	client := testASCClient(t, server.Client())
	client.baseURL = server.URL
	identity, err := createMacInstallerIdentity(context.Background(), client)
	if err != nil {
		t.Fatalf("create installer identity: %v", err)
	}
	if requestedType != ascMacInstallerCertificateType || identity.CertificateID != "cert1" || identity.Password == "" {
		t.Errorf("identity = %+v, requested %q", identity, requestedType)
	}
	p12, err := base64.StdEncoding.DecodeString(identity.P12)
	if err != nil {
		t.Fatal(err)
	}
	key, certificate, err := pkcs12.Decode(p12, identity.Password)
	if err != nil {
		t.Fatalf("decode PKCS#12: %v", err)
	}
	if private, ok := key.(*rsa.PrivateKey); !ok || !private.PublicKey.Equal(certificate.PublicKey) {
		t.Error("PKCS#12 key does not match its certificate")
	}

	recipientIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	if err := encryptMacInstallerIdentity(recipientIdentity.Recipient().String(), identity, output); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := os.Open(filepath.Join(output, EncryptedInstallerIdentityName))
	if err != nil {
		t.Fatal(err)
	}
	defer ciphertext.Close()
	reader, err := age.Decrypt(ciphertext, recipientIdentity)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip macInstallerIdentity
	if err := json.NewDecoder(reader).Decode(&roundTrip); err != nil || roundTrip != *identity {
		t.Errorf("round trip = %+v, %v", roundTrip, err)
	}
}

func TestCreateMacInstallerIdentityRejectsAnotherKey(t *testing.T) {
	other := mustRSAKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		template := &x509.Certificate{SerialNumber: big.NewInt(9), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &other.PublicKey, other)
		if err != nil {
			t.Fatal(err)
		}
		writeASCJSON(w, `{"data":{"type":"certificates","id":"cert1","attributes":{"certificateType":"MAC_INSTALLER_DISTRIBUTION","certificateContent":"`+
			base64.StdEncoding.EncodeToString(der)+`"}}}`)
	}))
	defer server.Close()
	client := testASCClient(t, server.Client())
	client.baseURL = server.URL
	if _, err := createMacInstallerIdentity(context.Background(), client); err == nil {
		t.Fatal("a certificate for another key was accepted")
	}
}

func mustRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func sha1Hex(data []byte) string {
	sum := sha1.Sum(data) // #nosec G401 -- mirrors Apple's signing certificate fingerprint.
	return hex.EncodeToString(sum[:])
}

func TestNormalizeBundlePermissions(t *testing.T) {
	app := writeMacOSApp(t, t.TempDir())
	profile := filepath.Join(app, "Contents", "embedded.provisionprofile")
	if err := os.WriteFile(profile, []byte("profile"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(app, "Contents", "MacOS"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(app, "Contents", "MacOS", "App"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := normalizeBundlePermissions(app); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		profile: 0644,
		filepath.Join(app, "Contents", "Info.plist"):   0644,
		filepath.Join(app, "Contents", "MacOS"):        0755,
		filepath.Join(app, "Contents", "MacOS", "App"): 0755,
		app: 0755,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", path, info.Mode().Perm(), want)
		}
	}
}
