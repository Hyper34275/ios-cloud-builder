package runner

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
)

func testASCClient(t *testing.T, httpClient *http.Client) *appStoreConnectClient {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemValue := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	client, err := newAppStoreConnectClient("KEY1234567", "issuer", string(pemValue))
	if err != nil {
		t.Fatal(err)
	}
	if httpClient != nil {
		client.httpClient = httpClient
	}
	return client
}

func writeASCJSON(w http.ResponseWriter, value string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(value))
}

func TestAppStoreConnectClientSignsAThreePartJWT(t *testing.T) {
	client := testASCClient(t, nil)
	token, err := client.token()
	if err != nil {
		t.Fatal(err)
	}
	if parts := strings.Split(token, "."); len(parts) != 3 {
		t.Fatalf("token = %q, want three dot-separated parts", token)
	}
}

func TestNewAppStoreConnectClientAcceptsBase64EncodedKey(t *testing.T) {
	pemClient := testASCClient(t, nil)
	der, err := x509.MarshalPKCS8PrivateKey(pemClient.privateKey)
	if err != nil {
		t.Fatal(err)
	}
	pemValue := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := newAppStoreConnectClient("KEY1234567", "issuer", base64.StdEncoding.EncodeToString(pemValue)); err != nil {
		t.Fatalf("newAppStoreConnectClient(base64) = %v", err)
	}
}

func TestNewAppStoreConnectClientRejectsGarbage(t *testing.T) {
	if _, err := newAppStoreConnectClient("KEY1234567", "issuer", "not a key"); err == nil {
		t.Fatal("newAppStoreConnectClient accepted a non-key value")
	}
}
