package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"howett.net/plist"
)

// EncryptedInstallerIdentityName is the deployment artifact member that
// carries a Mac installer identity created during the deployment, encrypted
// to the caller.
const EncryptedInstallerIdentityName = "mac-installer-identity.json.age"

const (
	appleDistributionPrefix = "Apple Distribution: "
	// macSandboxEntitlementPrefix selects the entitlements a macOS application
	// declares for itself: the App Sandbox and its exceptions. Everything an
	// App ID grants comes from the provisioning profile instead.
	macSandboxEntitlementPrefix = "com.apple.security."
	macAppSandboxEntitlement    = "com.apple.security.app-sandbox"
)

// macInstallerPrefixes are the common-name prefixes of a Mac Installer
// Distribution identity: the current name and the one Apple used before.
var macInstallerPrefixes = []string{"3rd Party Mac Developer Installer: ", "Mac Installer Distribution: "}

// deployMacAppStore signs an unsigned macOS application for the Mac App Store
// and uploads it as a signed installer package. It mirrors deployTestFlight:
// the application comes only from the authenticated encrypted intermediate,
// no project code runs, and every credential lives in a disposable keychain
// and workspace.
//
// The application signs with the same Apple Distribution identity as iOS,
// under a MAC_APP_STORE profile discovered or created through the App Store
// Connect API. Its entitlements are the profile's application and team
// identifiers plus the com.apple.security.* entitlements (the App Sandbox and
// its exceptions) the project declared by ad-hoc signing the bundle it built.
// The package signs with the Mac Installer Distribution identity in
// MAC_INSTALLER_P12; without one, a new identity is created through the API
// and returned in *created for the caller to store.
func deployMacAppStore(ctx context.Context, options *TestFlightOptions, credentials *appleCredentials, privateLog io.Writer, created **macInstallerIdentity) error {
	if credentials.issuerID == "" {
		return fmt.Errorf("a macOS deployment provisions through App Store Connect and requires ASC_ISSUER_ID")
	}
	workRoot, err := os.MkdirTemp(filepath.Dir(options.LogPath), ".macappstore-")
	if err != nil {
		return fmt.Errorf("prepare deployment workspace")
	}
	defer func() { _ = os.RemoveAll(workRoot) }()
	privateHome := filepath.Join(workRoot, "home")
	if err := os.Mkdir(privateHome, 0700); err != nil {
		return fmt.Errorf("prepare isolated deployment home")
	}
	signingHome := strings.TrimSpace(os.Getenv("HOME"))
	if !filepath.IsAbs(signingHome) {
		return fmt.Errorf("prepare signing environment")
	}
	run := executor{ctx: ctx, env: ChildEnvironment(workRoot, signingHome), log: privateLog}
	uploadRun := executor{ctx: ctx, env: ChildEnvironment(workRoot, privateHome), log: privateLog}

	transport, err := age.ParseX25519Identity(credentials.ageIdentity)
	if err != nil {
		return fmt.Errorf("parse signing transport identity")
	}
	unsignedPackage := filepath.Join(workRoot, "unsigned.ipa")
	if err := decryptFileBounded(transport, options.EncryptedIPAPath, unsignedPackage, maxDeployIPABytes); err != nil {
		return err
	}
	appPath, err := extractUnsignedIPA(unsignedPackage, filepath.Join(workRoot, "unsigned"))
	_ = os.Remove(unsignedPackage)
	if err != nil {
		return err
	}
	if err := checkMacOSBundleLayout(appPath); err != nil {
		return err
	}
	infoPath := filepath.Join(appPath, "Contents", "Info.plist")
	if err := setBundleBuildNumber(infoPath, options.BuildNumber); err != nil {
		return err
	}
	if err := setExportComplianceIfUnset(infoPath); err != nil {
		return err
	}
	bundleID, err := readBundleID(infoPath)
	if err != nil {
		return err
	}
	if !bundleIDPattern.MatchString(bundleID) {
		return fmt.Errorf("invalid application bundle identifier")
	}
	sandbox, err := readMacSandboxEntitlements(run, workRoot, appPath)
	if err != nil {
		return err
	}
	publisher, err := newAppStoreConnectClient(credentials.apiKeyID, credentials.issuerID, credentials.apiKey)
	if err != nil {
		return err
	}

	secretsDir := filepath.Join(workRoot, "credentials")
	if err := os.Mkdir(secretsDir, 0700); err != nil {
		return fmt.Errorf("prepare credential files")
	}
	p12Path := filepath.Join(secretsDir, "distribution.p12")
	installerPath := filepath.Join(secretsDir, "installer.p12")
	apiKeyPath := filepath.Join(workRoot, "private_keys", "AuthKey_"+credentials.apiKeyID+".p8")
	if err := writeBase64Secret(credentials.p12, p12Path); err != nil {
		return fmt.Errorf("decode distribution certificate")
	}
	if err := writeTextOrBase64Secret(credentials.apiKey, apiKeyPath, "PRIVATE KEY"); err != nil {
		return fmt.Errorf("decode App Store Connect key")
	}
	installerP12, installerPassword := credentials.installerP12, credentials.installerP12Password
	if installerP12 == "" {
		identity, createErr := createMacInstallerIdentity(ctx, publisher)
		if createErr != nil {
			return createErr
		}
		*created = identity
		installerP12, installerPassword = identity.P12, identity.Password
		_, _ = fmt.Fprintln(privateLog, "Created a Mac Installer Distribution certificate through the App Store Connect API.\n"+
			"It is returned encrypted; store it as MAC_INSTALLER_P12 and MAC_INSTALLER_P12_PASSWORD in apple-production.")
	}
	if err := writeBase64Secret(installerP12, installerPath); err != nil {
		return fmt.Errorf("decode Mac installer certificate")
	}

	keychainPath := filepath.Join(workRoot, "signing.keychain-db")
	keychainPassword, err := randomPassword()
	if err != nil {
		return fmt.Errorf("create temporary keychain password")
	}
	defer func() { _ = run.runSecurity(workRoot, "delete-keychain", keychainPath) }()
	for _, args := range [][]string{
		{"create-keychain", "-p", keychainPassword, keychainPath},
		{"set-keychain-settings", "-lut", "21600", keychainPath},
		{"unlock-keychain", "-p", keychainPassword, keychainPath},
		{"import", p12Path, "-P", credentials.p12Password, "-T", "/usr/bin/codesign", "-t", "cert", "-f", "pkcs12", "-k", keychainPath},
		{"import", installerPath, "-P", installerPassword, "-T", "/usr/bin/productbuild", "-t", "cert", "-f", "pkcs12", "-k", keychainPath},
		{"set-key-partition-list", "-S", "apple-tool:,apple:,codesign:", "-s", "-k", keychainPassword, keychainPath},
		{"list-keychains", "-d", "user", "-s", keychainPath},
	} {
		if err := run.runSecurity(workRoot, args...); err != nil {
			return err
		}
	}
	codesigning, err := run.capture(workRoot, "/usr/bin/security", "find-identity", "-v", "-p", "codesigning", keychainPath)
	if err != nil {
		return err
	}
	identityFingerprint, signingIdentity, ok := findSigningIdentity(string(codesigning), credentials.teamID, appleDistributionPrefix)
	if !ok {
		return fmt.Errorf("distribution signing identity was not imported")
	}
	basic, err := run.capture(workRoot, "/usr/bin/security", "find-identity", "-v", "-p", "basic", keychainPath)
	if err != nil {
		return err
	}
	_, installerIdentity, ok := findSigningIdentity(string(basic), credentials.teamID, macInstallerPrefixes...)
	if !ok {
		return fmt.Errorf("the Mac installer identity was not imported or belongs to another team")
	}

	parseCandidate := func(candidatePath string) (provisioningProfileCandidate, error) {
		profileOutput, captureErr := run.capture(workRoot, "/usr/bin/security", "cms", "-D", "-i", candidatePath, "-k", keychainPath)
		if captureErr != nil {
			return provisioningProfileCandidate{}, captureErr
		}
		var candidate provisioningProfile
		if _, unmarshalErr := plist.Unmarshal(profileOutput, &candidate); unmarshalErr != nil {
			return provisioningProfileCandidate{}, fmt.Errorf("parse provisioning profile")
		}
		return provisioningProfileCandidate{path: candidatePath, profile: candidate}, nil
	}
	profilesDir := filepath.Join(secretsDir, "profiles-app")
	if err := os.Mkdir(profilesDir, 0700); err != nil {
		return fmt.Errorf("prepare provisioning profile workspace")
	}
	bundleResourceID, downloaded, err := downloadASCPlatformProfiles(ctx, publisher, macProvisioning, bundleID, profilesDir)
	if err != nil {
		return err
	}
	var candidates []provisioningProfileCandidate
	for _, candidatePath := range downloaded {
		candidate, parseErr := parseCandidate(candidatePath)
		if parseErr != nil {
			return parseErr
		}
		candidates = append(candidates, candidate)
	}
	selected, err := selectMacProvisioningProfile(candidates, credentials.teamID, bundleID, identityFingerprint)
	if err != nil {
		createdPath, createErr := createASCPlatformProfile(ctx, publisher, macProvisioning, bundleResourceID, bundleID, identityFingerprint, profilesDir)
		if createErr != nil {
			return createErr
		}
		createdProfile, parseErr := parseCandidate(createdPath)
		if parseErr != nil {
			return parseErr
		}
		selected, err = selectMacProvisioningProfile(append(candidates, createdProfile), credentials.teamID, bundleID, identityFingerprint)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(privateLog, "Created a Mac App Store provisioning profile for %s through the App Store Connect API.\n", bundleID)
	}
	if err := copyPrivateFile(selected.path, filepath.Join(appPath, "Contents", "embedded.provisionprofile")); err != nil {
		return fmt.Errorf("embed provisioning profile")
	}
	entitlements, err := macSigningEntitlements(&selected.profile, credentials.teamID, sandbox)
	if err != nil {
		return err
	}
	entitlementsPath := filepath.Join(secretsDir, "entitlements.plist")
	encoded, err := plist.Marshal(entitlements, plist.XMLFormat)
	if err != nil || os.WriteFile(entitlementsPath, encoded, 0600) != nil {
		return fmt.Errorf("prepare signing entitlements")
	}
	if err := normalizeBundlePermissions(appPath); err != nil {
		return err
	}
	application := signingTarget{path: appPath, entitlementsPath: entitlementsPath}
	if err := signApplication(run, application, nil, signingIdentity, keychainPath); err != nil {
		return err
	}

	packagePath := filepath.Join(workRoot, "App.pkg")
	if err := run.run(filepath.Dir(appPath), "/usr/bin/productbuild", "--component", appPath, "/Applications",
		"--sign", installerIdentity, "--keychain", keychainPath, packagePath); err != nil {
		return err
	}
	if info, err := os.Stat(packagePath); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("installer packaging produced no output")
	}
	if err := run.run(workRoot, "/usr/sbin/pkgutil", "--check-signature", packagePath); err != nil {
		return err
	}
	if err := uploadRun.runAltool(workRoot, altoolPlatformArgs("--validate-app", packagePath, "macos", credentials)...); err != nil {
		return fmt.Errorf("validation with App Store Connect failed")
	}
	if err := uploadRun.runAltool(workRoot, altoolPlatformArgs("--upload-app", packagePath, "macos", credentials)...); err != nil {
		return fmt.Errorf("upload to App Store Connect failed")
	}
	_, _ = fmt.Fprintln(privateLog, "App Store Connect accepted the signed installer package upload.")
	return nil
}

// checkMacOSBundleLayout requires a macOS application bundle and refuses
// nested applications, extensions, XPC services, login items and system
// extensions: each would need a profile and entitlements of its own, which
// this runner does not provide, so they fail closed instead of being uploaded
// half-signed. Frameworks, dynamic libraries and resource bundles are fine.
func checkMacOSBundleLayout(appPath string) error {
	if info, err := os.Lstat(filepath.Join(appPath, "Contents", "Info.plist")); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("the unsigned package does not hold a macOS application (no Contents/Info.plist)")
	}
	if info, err := os.Lstat(filepath.Join(appPath, "Contents", "MacOS")); err != nil || !info.IsDir() {
		return fmt.Errorf("the macOS application has no Contents/MacOS directory")
	}
	return filepath.WalkDir(appPath, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if current == appPath || !entry.IsDir() {
			return nil
		}
		name := strings.ToLower(entry.Name())
		for _, suffix := range []string{".app", ".appex", ".xpc", ".systemextension", ".kext"} {
			if strings.HasSuffix(name, suffix) {
				return fmt.Errorf("nested %s bundles in a macOS application are not supported", suffix)
			}
		}
		return nil
	})
}

// normalizeBundlePermissions makes every directory and file in the bundle
// readable by everyone, keeping only the executable bits it already has.
// Extraction and the embedded profile write private (0700/0600) modes, and
// App Store Connect refuses an installer package holding files only root can
// read, because macOS could not verify the signature for other users.
func normalizeBundlePermissions(appPath string) error {
	return filepath.WalkDir(appPath, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect application file permissions")
		}
		mode := os.FileMode(0644)
		switch {
		case entry.IsDir():
			mode = 0755
		case !info.Mode().IsRegular():
			return fmt.Errorf("the macOS application contains a special file")
		case info.Mode().Perm()&0100 != 0:
			mode = 0755
		}
		if err := os.Chmod(current, mode); err != nil {
			return fmt.Errorf("set application file permissions")
		}
		return nil
	})
}

// readMacSandboxEntitlements reads the entitlements the project signed its
// application with (an ad-hoc signature is enough) and keeps only the App
// Sandbox ones. The Mac App Store requires the App Sandbox, so an application
// without it fails here rather than in App Store review.
func readMacSandboxEntitlements(run executor, dir, appPath string) (map[string]any, error) {
	output, err := run.capture(dir, "/usr/bin/codesign", "-d", "--entitlements", "-", "--xml", appPath)
	if err != nil {
		return nil, fmt.Errorf("read the application's entitlements; the build script must sign it ad hoc with its sandbox entitlements (codesign --sign - --entitlements <file>)")
	}
	return macSandboxEntitlements(output)
}

// macSandboxEntitlements parses an entitlements plist and returns its
// com.apple.security.* entries, requiring com.apple.security.app-sandbox.
func macSandboxEntitlements(data []byte) (map[string]any, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("the macOS application declares no entitlements; the Mac App Store requires %s", macAppSandboxEntitlement)
	}
	var declared map[string]any
	if _, err := plist.Unmarshal(data, &declared); err != nil {
		return nil, fmt.Errorf("parse the application's entitlements")
	}
	sandbox := make(map[string]any)
	for key, value := range declared {
		if strings.HasPrefix(key, macSandboxEntitlementPrefix) {
			sandbox[key] = value
		}
	}
	if enabled, _ := sandbox[macAppSandboxEntitlement].(bool); !enabled {
		return nil, fmt.Errorf("the Mac App Store requires %s to be true", macAppSandboxEntitlement)
	}
	return sandbox, nil
}

// macSigningEntitlements combines what the profile grants (the application
// and team identifiers) with the application's own sandbox entitlements.
func macSigningEntitlements(profile *provisioningProfile, teamID string, sandbox map[string]any) (map[string]any, error) {
	applicationID, _ := profile.Entitlements["com.apple.application-identifier"].(string)
	team, _ := profile.Entitlements["com.apple.developer.team-identifier"].(string)
	if applicationID == "" || team != teamID {
		return nil, fmt.Errorf("provisioning profile lacks the application or team identifier")
	}
	entitlements := make(map[string]any, len(sandbox)+2)
	for key, value := range sandbox {
		entitlements[key] = value
	}
	entitlements["com.apple.application-identifier"] = applicationID
	entitlements["com.apple.developer.team-identifier"] = teamID
	return entitlements, nil
}

// selectMacProvisioningProfile is selectProvisioningProfile for Mac App Store
// profiles, whose application identifier entitlement has its own key.
func selectMacProvisioningProfile(candidates []provisioningProfileCandidate, teamID, bundleID, identityFingerprint string) (provisioningProfileCandidate, error) {
	var matching []provisioningProfileCandidate
	for i := range candidates {
		profile := &candidates[i].profile
		if profile.UUID == "" || len(profile.TeamIdentifier) != 1 || profile.TeamIdentifier[0] != teamID {
			return provisioningProfileCandidate{}, fmt.Errorf("provisioning profile does not match APPLE_TEAM_ID")
		}
		applicationID, _ := profile.Entitlements["com.apple.application-identifier"].(string)
		if profileMatchesBundle(applicationID, teamID, bundleID) {
			matching = append(matching, candidates[i])
		}
	}
	if len(matching) == 0 {
		return provisioningProfileCandidate{}, fmt.Errorf("no Mac App Store provisioning profile matches the application bundle identifier")
	}
	sort.SliceStable(matching, func(i, j int) bool {
		return matching[i].profile.ExpirationDate.After(matching[j].profile.ExpirationDate)
	})
	for i := range matching {
		if validateMacAppStoreProfile(&matching[i].profile) == nil &&
			profileAuthorizesIdentity(matching[i].profile.DeveloperCertificates, identityFingerprint) {
			return matching[i], nil
		}
	}
	return provisioningProfileCandidate{}, fmt.Errorf("no current Mac App Store provisioning profile authorizes the distribution certificate")
}

func validateMacAppStoreProfile(profile *provisioningProfile) error {
	if profile.ExpirationDate.IsZero() || !profile.ExpirationDate.After(time.Now().Add(5*time.Minute)) {
		return fmt.Errorf("provisioning profile is expired or near expiry")
	}
	supportsMacOS := false
	for _, platform := range profile.Platform {
		if platform == "OSX" {
			supportsMacOS = true
		}
	}
	if !supportsMacOS || len(profile.DeveloperCertificates) != 1 || len(profile.ProvisionedDevices) != 0 || profile.ProvisionsAllDevices {
		return fmt.Errorf("provisioning profile is not a Mac App Store distribution profile")
	}
	if getTaskAllow, ok := profile.Entitlements["com.apple.security.get-task-allow"]; ok && getTaskAllow != false {
		return fmt.Errorf("provisioning profile allows debugging")
	}
	return nil
}

// findSigningIdentity returns the fingerprint and name of the first valid
// identity in security find-identity output whose name has one of the
// prefixes and ends with the team identifier.
func findSigningIdentity(output, teamID string, prefixes ...string) (string, string, bool) {
	for _, match := range identityPattern.FindAllStringSubmatch(output, -1) {
		if !strings.HasSuffix(match[2], "("+teamID+")") {
			continue
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(match[2], prefix) {
				return match[1], match[2], true
			}
		}
	}
	return "", "", false
}

// encryptMacInstallerIdentity writes a created installer identity, encrypted
// to the caller, next to the encrypted deployment log.
func encryptMacInstallerIdentity(recipientText string, identity *macInstallerIdentity, outputDir string) (retErr error) {
	recipient, err := age.ParseX25519Recipient(recipientText)
	if err != nil {
		return err
	}
	plaintext, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	destination := filepath.Join(outputDir, EncryptedInstallerIdentityName)
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); retErr == nil {
			retErr = closeErr
		}
		if retErr != nil {
			_ = os.Remove(destination)
		}
	}()
	writer, err := age.Encrypt(file, recipient)
	if err != nil {
		return err
	}
	if _, err := writer.Write(plaintext); err != nil {
		return err
	}
	return writer.Close()
}
