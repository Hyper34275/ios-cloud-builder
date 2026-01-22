// Package auth provides GitHub OAuth authentication via Device Code flow.
// Tokens are securely stored in the OS keychain using go-keyring.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	clientID = "Ov23livITjYL1eNTuE7a"

	keyringService = "builder-cli"
	keyringUser    = "github-token"
)

// Token represents a GitHub OAuth access token.
type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

// DeviceCode represents the response from GitHub's device code request.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// Login performs GitHub OAuth Device Code flow authentication.
// It displays a URL and code for the user to authorize, then stores the token in the keychain.
func Login(ctx context.Context) (*Token, error) {
	code, err := requestDeviceCode(ctx)
	if err != nil {
		return nil, err
	}

	fmt.Println()
	fmt.Printf("  Open: %s\n", code.VerificationURI)
	fmt.Printf("  Enter code: %s\n", code.UserCode)
	fmt.Println()
	fmt.Println("Waiting for authorization...")

	token, err := pollForToken(ctx, code)
	if err != nil {
		return nil, err
	}

	if err := keyring.Set(keyringService, keyringUser, token.AccessToken); err != nil {
		return nil, fmt.Errorf("failed to store token: %w", err)
	}

	return token, nil
}

// ErrNotAuthenticated indicates no stored authentication token was found.
var ErrNotAuthenticated = errors.New("not authenticated")

// GetToken retrieves the stored GitHub token from the OS keychain.
func GetToken() (string, error) {
	token, err := keyring.Get(keyringService, keyringUser)
	if err != nil {
		if err == keyring.ErrNotFound {
			return "", ErrNotAuthenticated
		}
		return "", fmt.Errorf("failed to get token: %w", err)
	}
	return token, nil
}

// Logout removes the stored GitHub token from the OS keychain.
func Logout() error {
	err := keyring.Delete(keyringService, keyringUser)
	if err != nil && err != keyring.ErrNotFound {
		return fmt.Errorf("failed to delete token: %w", err)
	}
	return nil
}

func requestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	data := url.Values{
		"client_id": {clientID},
		"scope":     {"repo workflow"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/device/code", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request device code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed: status %d", resp.StatusCode)
	}

	var code DeviceCode
	if err := json.NewDecoder(resp.Body).Decode(&code); err != nil {
		return nil, fmt.Errorf("failed to parse device code response: %w", err)
	}

	if code.DeviceCode == "" {
		return nil, fmt.Errorf("empty device code in response")
	}

	return &code, nil
}

func pollForToken(ctx context.Context, code *DeviceCode) (*Token, error) {
	interval := time.Duration(code.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}

	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("authorization timed out")
		}

		token, err := requestToken(ctx, code.DeviceCode)
		if err == nil {
			return token, nil
		}

		if strings.Contains(err.Error(), "authorization_pending") {
			continue
		}
		if strings.Contains(err.Error(), "slow_down") {
			interval += 5 * time.Second
			continue
		}

		return nil, err
	}
}

func requestToken(ctx context.Context, deviceCode string) (*Token, error) {
	data := url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request token: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Token
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if result.Error != "" {
		return nil, fmt.Errorf("%s: %s", result.Error, result.ErrorDescription)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("empty access token in response")
	}

	return &result.Token, nil
}
