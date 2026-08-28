package runner

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	appStoreConnectRoot = "https://api.appstoreconnect.apple.com"
	maxAppStoreResponse = int64(4 * 1024 * 1024)
)

var bundleIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{1,254}$`)

type ascResource struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Attributes json.RawMessage `json:"attributes"`
}

type ascListResponse struct {
	Data []ascResource `json:"data"`
}

type ascSingleResponse struct {
	Data *ascResource `json:"data"`
}

// appStoreConnectClient signs its own short-lived JWTs per request, so it
// never holds a reusable bearer token the private log could leak.
type appStoreConnectClient struct {
	baseURL    string
	httpClient *http.Client
	keyID      string
	issuerID   string
	privateKey *ecdsa.PrivateKey
	now        func() time.Time
}

func newAppStoreConnectClient(keyID, issuerID, keyValue string) (*appStoreConnectClient, error) {
	keyBytes := []byte(strings.TrimSpace(keyValue))
	if !bytes.Contains(keyBytes, []byte("-----BEGIN PRIVATE KEY-----")) {
		decoded, err := decodeBase64Secret(keyValue)
		if err != nil {
			return nil, fmt.Errorf("decode App Store Connect key")
		}
		keyBytes = decoded
	}
	block, _ := pem.Decode(keyBytes)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("parse App Store Connect key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	privateKey, ok := parsed.(*ecdsa.PrivateKey)
	if err != nil || !ok || privateKey.Curve != elliptic.P256() {
		return nil, fmt.Errorf("parse App Store Connect key")
	}
	return &appStoreConnectClient{
		baseURL: appStoreConnectRoot, httpClient: &http.Client{Timeout: 60 * time.Second},
		keyID: keyID, issuerID: issuerID, privateKey: privateKey, now: time.Now,
	}, nil
}

func (c *appStoreConnectClient) token() (string, error) {
	now := c.now().Unix()
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": c.keyID, "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{
		"iss": c.issuerID, "iat": now, "exp": now + 900, "aud": "appstoreconnect-v1",
	})
	encode := base64.RawURLEncoding.EncodeToString
	unsigned := encode(header) + "." + encode(payload)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, c.privateKey, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign App Store Connect request")
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return unsigned + "." + encode(signature), nil
}

func (c *appStoreConnectClient) request(ctx context.Context, method, apiPath string, query url.Values, body any, output any) error {
	requestURL := c.baseURL + apiPath
	if len(query) != 0 {
		requestURL += "?" + query.Encode()
	}
	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("prepare App Store Connect request")
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, bodyReader)
	if err != nil {
		return fmt.Errorf("prepare App Store Connect request")
	}
	token, err := c.token()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("contact App Store Connect: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxAppStoreResponse+1))
	if err != nil || int64(len(contents)) > maxAppStoreResponse {
		return fmt.Errorf("read App Store Connect response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return appStoreConnectHTTPError(response.StatusCode, contents)
	}
	if output != nil && len(contents) != 0 {
		if err := json.Unmarshal(contents, output); err != nil {
			return fmt.Errorf("parse App Store Connect response")
		}
	}
	return nil
}

func appStoreConnectHTTPError(status int, contents []byte) error {
	var envelope struct {
		Errors []struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(contents, &envelope)
	var details []string
	for _, item := range envelope.Errors {
		detail := strings.TrimSpace(item.Title + ": " + item.Detail)
		if len(detail) > 500 {
			detail = detail[:500]
		}
		if detail != ":" {
			details = append(details, detail)
		}
	}
	if len(details) == 0 {
		return fmt.Errorf("request to App Store Connect failed with HTTP %d", status)
	}
	return fmt.Errorf("request to App Store Connect failed with HTTP %d: %s", status, strings.Join(details, " | "))
}
