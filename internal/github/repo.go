package github

import (
	"context"
	"fmt"
	"net/http"
)

// GetRepository retrieves a repository by owner and name
func (c *Client) GetRepository(ctx context.Context, owner, repo string) (*Repository, error) {
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)

	var repository Repository
	if err := c.do(ctx, path, &repository); err != nil {
		return nil, fmt.Errorf("failed to get repository: %w", err)
	}

	return &repository, nil
}

// GetPublicKey retrieves the repository's public key for encrypting secrets
func (c *Client) GetPublicKey(ctx context.Context, owner, repo string) (*PublicKey, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/secrets/public-key", owner, repo)

	var key PublicKey
	if err := c.do(ctx, path, &key); err != nil {
		return nil, fmt.Errorf("failed to get public key: %w", err)
	}

	return &key, nil
}

// CreateOrUpdateSecret creates or updates a repository secret
// The value should be encrypted using the repository's public key
func (c *Client) CreateOrUpdateSecret(ctx context.Context, owner, repo, name, encryptedValue, keyID string) error {
	path := fmt.Sprintf("/repos/%s/%s/actions/secrets/%s", owner, repo, name)

	req := CreateSecretRequest{
		EncryptedValue: encryptedValue,
		KeyID:          keyID,
	}

	resp, err := c.request(ctx, "PUT", path, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to create secret: status %d", resp.StatusCode)
	}

	return nil
}
