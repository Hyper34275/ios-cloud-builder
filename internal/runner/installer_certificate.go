package runner

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"

	"software.sslmate.com/src/go-pkcs12"
)

const ascMacInstallerCertificateType = "MAC_INSTALLER_DISTRIBUTION"

// macInstallerIdentity is a Mac Installer Distribution identity created
// through the App Store Connect API. Its private key exists only here, so the
// identity is handed back, encrypted to the caller, to be stored in the
// protected Environment as MAC_INSTALLER_P12 and MAC_INSTALLER_P12_PASSWORD;
// later deployments then reuse it instead of creating another certificate.
type macInstallerIdentity struct {
	P12           string `json:"p12"`      // base64 PKCS#12
	Password      string `json:"password"` // the PKCS#12 password
	CertificateID string `json:"certificateId"`
}

// createMacInstallerIdentity generates a key pair, has App Store Connect
// issue a Mac Installer Distribution certificate for it, and returns both as
// a PKCS#12 file with a random password. The legacy PKCS#12 algorithms are
// used because they are what security(1) imports on every supported macOS.
func createMacInstallerIdentity(ctx context.Context, api *appStoreConnectClient) (*macInstallerIdentity, error) {
	if api == nil {
		return nil, fmt.Errorf("creating a Mac installer certificate requires ASC_ISSUER_ID")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate Mac installer key")
	}
	request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "ios-cloud-builder Mac Installer"},
	}, key)
	if err != nil {
		return nil, fmt.Errorf("create Mac installer certificate request")
	}
	body := map[string]any{"data": map[string]any{
		"type": "certificates",
		"attributes": map[string]any{
			"certificateType": ascMacInstallerCertificateType,
			"csrContent":      string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: request})),
		},
	}}
	var response ascSingleResponse
	if err := api.request(ctx, http.MethodPost, "/v1/certificates", nil, body, &response); err != nil {
		return nil, fmt.Errorf("create Mac installer certificate: %w", err)
	}
	if response.Data == nil || response.Data.Type != "certificates" || response.Data.ID == "" {
		return nil, fmt.Errorf("no created certificate returned by App Store Connect")
	}
	var attributes struct {
		CertificateType    string `json:"certificateType"`
		CertificateContent string `json:"certificateContent"`
	}
	if json.Unmarshal(response.Data.Attributes, &attributes) != nil || attributes.CertificateType != ascMacInstallerCertificateType {
		return nil, fmt.Errorf("parse created Mac installer certificate")
	}
	der, err := decodeASCBase64(attributes.CertificateContent, maxProfileBytes)
	if err != nil {
		return nil, fmt.Errorf("decode created Mac installer certificate")
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse created Mac installer certificate")
	}
	if public, ok := certificate.PublicKey.(*rsa.PublicKey); !ok || !public.Equal(&key.PublicKey) {
		return nil, fmt.Errorf("created Mac installer certificate does not match the generated key")
	}
	password, err := randomPassword()
	if err != nil {
		return nil, fmt.Errorf("create Mac installer identity password")
	}
	p12, err := pkcs12.LegacyDES.Encode(key, certificate, nil, password)
	if err != nil {
		return nil, fmt.Errorf("encode Mac installer identity")
	}
	return &macInstallerIdentity{
		P12:           base64.StdEncoding.EncodeToString(p12),
		Password:      password,
		CertificateID: response.Data.ID,
	}, nil
}
