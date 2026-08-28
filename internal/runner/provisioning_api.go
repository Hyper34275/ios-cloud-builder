package runner

import (
	"context"
	"crypto/sha1" // #nosec G505 -- Apple identifies signing certificates by SHA-1 fingerprint.
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const ascAppStoreProfileType = "IOS_APP_STORE"

type ascProfileAttributes struct {
	Name           string `json:"name"`
	ProfileType    string `json:"profileType"`
	ProfileState   string `json:"profileState"`
	ProfileContent string `json:"profileContent"`
}

// downloadASCProvisioningProfiles finds bundleID's App Store Connect bundle
// identifier and returns every active App Store profile registered for it,
// so a project can be signed without a provisioning profile ever having to
// be manually fetched and stored as a secret.
func downloadASCProvisioningProfiles(ctx context.Context, api *appStoreConnectClient, bundleID, destinationDir string) (string, []string, error) {
	if api == nil || !bundleIDPattern.MatchString(bundleID) {
		return "", nil, fmt.Errorf("invalid App Store Connect provisioning metadata")
	}
	bundleResourceID, err := findASCBundleID(ctx, api, bundleID)
	if err != nil {
		return "", nil, err
	}
	query := url.Values{
		"fields[profiles]": {"name,profileType,profileState,profileContent"},
		"limit":            {"200"},
	}
	var response ascListResponse
	apiPath := "/v1/bundleIds/" + url.PathEscape(bundleResourceID) + "/profiles"
	if err := api.request(ctx, http.MethodGet, apiPath, query, nil, &response); err != nil {
		return "", nil, err
	}
	paths := make([]string, 0, len(response.Data))
	for index, resource := range response.Data {
		var attributes ascProfileAttributes
		if resource.Type != "profiles" || json.Unmarshal(resource.Attributes, &attributes) != nil {
			return "", nil, fmt.Errorf("parse App Store Connect provisioning profile")
		}
		if attributes.ProfileType != ascAppStoreProfileType || attributes.ProfileState != "ACTIVE" {
			continue
		}
		profilePath := filepath.Join(destinationDir, fmt.Sprintf("profile-asc-%03d.mobileprovision", index))
		if err := writeASCProfileContent(attributes.ProfileContent, profilePath); err != nil {
			return "", nil, err
		}
		paths = append(paths, profilePath)
	}
	return bundleResourceID, paths, nil
}

// findASCBundleID looks up bundleID's Identifiers resource, registering it
// when the identifier itself does not exist yet. Every App Store Connect app
// is backed by one of these regardless of whether it was created from the
// Developer Portal, from "New App" in App Store Connect, or here.
func findASCBundleID(ctx context.Context, api *appStoreConnectClient, bundleID string) (string, error) {
	query := url.Values{
		"filter[identifier]": {bundleID},
		"fields[bundleIds]":  {"identifier,platform"},
		"limit":              {"200"},
	}
	var response ascListResponse
	if err := api.request(ctx, http.MethodGet, "/v1/bundleIds", query, nil, &response); err != nil {
		return "", err
	}
	var exact string
	for _, resource := range response.Data {
		var attributes struct {
			Identifier string `json:"identifier"`
			Platform   string `json:"platform"`
		}
		if resource.Type != "bundleIds" || json.Unmarshal(resource.Attributes, &attributes) != nil {
			return "", fmt.Errorf("parse App Store Connect bundle identifier")
		}
		if attributes.Identifier == bundleID && (attributes.Platform == "IOS" || attributes.Platform == "UNIVERSAL") {
			if exact != "" {
				return "", fmt.Errorf("multiple App Store Connect bundle identifiers matched exactly")
			}
			exact = resource.ID
		}
	}
	if exact != "" {
		return exact, nil
	}
	return createASCBundleID(ctx, api, bundleID)
}

// createASCBundleID registers bundleID as a plain Identifier with no extra
// capabilities. A project needing push notifications, Sign in with Apple, or
// any other capability must register its own Identifier in the Developer
// Portal first; this only covers the common case of a bare application ID.
func createASCBundleID(ctx context.Context, api *appStoreConnectClient, bundleID string) (string, error) {
	body := map[string]any{"data": map[string]any{
		"type": "bundleIds",
		"attributes": map[string]any{
			"identifier": bundleID,
			"name":       bundleID,
			"platform":   "IOS",
		},
	}}
	var response ascSingleResponse
	if err := api.request(ctx, http.MethodPost, "/v1/bundleIds", nil, body, &response); err != nil {
		return "", fmt.Errorf("register App Store Connect bundle identifier: %w", err)
	}
	if response.Data == nil || response.Data.Type != "bundleIds" || response.Data.ID == "" {
		return "", fmt.Errorf("no created bundle identifier returned by App Store Connect")
	}
	return response.Data.ID, nil
}

func createASCProvisioningProfile(ctx context.Context, api *appStoreConnectClient, bundleResourceID, bundleID, identityFingerprint, destinationDir string) (string, error) {
	if api == nil || bundleResourceID == "" || !bundleIDPattern.MatchString(bundleID) || len(identityFingerprint) != 40 {
		return "", fmt.Errorf("invalid App Store Connect provisioning metadata")
	}
	certificateID, err := findASCCertificate(ctx, api, identityFingerprint)
	if err != nil {
		return "", err
	}
	body := map[string]any{"data": map[string]any{
		"type": "profiles",
		"attributes": map[string]any{
			"name":        "ios-cloud-builder " + bundleID,
			"profileType": ascAppStoreProfileType,
		},
		"relationships": map[string]any{
			"bundleId":     map[string]any{"data": map[string]string{"type": "bundleIds", "id": bundleResourceID}},
			"certificates": map[string]any{"data": []map[string]string{{"type": "certificates", "id": certificateID}}},
		},
	}}
	var response ascSingleResponse
	if err := api.request(ctx, http.MethodPost, "/v1/profiles", nil, body, &response); err != nil {
		return "", err
	}
	if response.Data == nil || response.Data.Type != "profiles" {
		return "", fmt.Errorf("no created provisioning profile returned by App Store Connect")
	}
	var attributes ascProfileAttributes
	if json.Unmarshal(response.Data.Attributes, &attributes) != nil || attributes.ProfileType != ascAppStoreProfileType {
		return "", fmt.Errorf("parse created App Store Connect provisioning profile")
	}
	profilePath := filepath.Join(destinationDir, "profile-asc-created.mobileprovision")
	if err := writeASCProfileContent(attributes.ProfileContent, profilePath); err != nil {
		return "", err
	}
	return profilePath, nil
}

func findASCCertificate(ctx context.Context, api *appStoreConnectClient, identityFingerprint string) (string, error) {
	query := url.Values{
		"fields[certificates]":    {"certificateType,certificateContent,expirationDate"},
		"filter[certificateType]": {"DISTRIBUTION,IOS_DISTRIBUTION"},
		"limit":                   {"200"},
	}
	var response ascListResponse
	if err := api.request(ctx, http.MethodGet, "/v1/certificates", query, nil, &response); err != nil {
		return "", err
	}
	for _, resource := range response.Data {
		var attributes struct {
			CertificateType    string    `json:"certificateType"`
			CertificateContent string    `json:"certificateContent"`
			ExpirationDate     time.Time `json:"expirationDate"`
		}
		if resource.Type != "certificates" || json.Unmarshal(resource.Attributes, &attributes) != nil {
			return "", fmt.Errorf("parse App Store Connect signing certificate")
		}
		// The API-side filter narrows the request; this repeats the check
		// locally so a permissive or misbehaving server cannot substitute a
		// certificate this code never asked to sign with.
		if attributes.CertificateType != "DISTRIBUTION" && attributes.CertificateType != "IOS_DISTRIBUTION" {
			continue
		}
		certificate, err := decodeASCBase64(attributes.CertificateContent, maxProfileBytes)
		if err != nil {
			return "", fmt.Errorf("decode App Store Connect signing certificate")
		}
		fingerprint := sha1.Sum(certificate) // #nosec G401 -- required to compare Apple's certificate fingerprint.
		if strings.EqualFold(hex.EncodeToString(fingerprint[:]), identityFingerprint) &&
			attributes.ExpirationDate.After(time.Now().Add(5*time.Minute)) {
			return resource.ID, nil
		}
	}
	return "", fmt.Errorf("no current App Store Connect certificate matches the imported distribution identity")
}

func writeASCProfileContent(value, destination string) error {
	data, err := decodeASCBase64(value, maxProfileBytes)
	if err != nil {
		return fmt.Errorf("decode App Store Connect provisioning profile")
	}
	return writePrivateFile(destination, data)
}

func decodeASCBase64(value string, limit int64) ([]byte, error) {
	if int64(len(value)) > limit*2 {
		return nil, fmt.Errorf("value is too large")
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("invalid base64 value")
	}
	return data, nil
}
