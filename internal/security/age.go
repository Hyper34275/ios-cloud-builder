// Package security provides local AGE identity management and artifact
// encryption for central builds. Private identities are kept in the operating
// system keyring when available, or in a mode-0600 user configuration file.
package security

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"filippo.io/age"
	"github.com/zalando/go-keyring"
)

const (
	keyringService = "builder-cli"
	keyringUser    = "age-x25519-identity"
	configDirName  = "ios-builder"
	identityFile   = "age-identity"
)

// ErrIdentityNotFound indicates that no local AGE identity has been created.
var ErrIdentityNotFound = errors.New("AGE identity not found")

var (
	errIdentityAlreadyExists = errors.New("AGE identity already exists")
	identityMu               sync.Mutex
)

type keyringBackend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
}

type systemKeyring struct{}

func (systemKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (systemKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

// IdentityStore manages the one local X25519 identity used to decrypt central
// build artifacts. Use NewIdentityStore for normal operation.
type IdentityStore struct {
	path       string
	keyring    keyringBackend
	useKeyring bool
}

// NewIdentityStore returns the platform-default identity store.
func NewIdentityStore() (*IdentityStore, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	return &IdentityStore{
		path:       filepath.Join(base, configDirName, identityFile),
		keyring:    systemKeyring{},
		useKeyring: keyringSessionUsable(),
	}, nil
}

// NewFileIdentityStore returns an identity store that deliberately bypasses
// the OS keyring. It is useful for portable/headless installations and tests.
func NewFileIdentityStore(path string) *IdentityStore {
	return &IdentityStore{path: path}
}

// keyringSessionUsable avoids calls that are known to hang on headless Linux
// and WSL. Desktop Linux is considered usable only with a D-Bus session.
func keyringSessionUsable() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	return os.Getenv("DBUS_SESSION_BUS_ADDRESS") != ""
}

// EnsureIdentity loads the local identity, or generates and securely stores a
// new one when this is the first central build.
func (s *IdentityStore) EnsureIdentity() (*age.X25519Identity, error) {
	identityMu.Lock()
	defer identityMu.Unlock()

	identity, err := s.Identity()
	if err == nil {
		return identity, nil
	}
	if !errors.Is(err, ErrIdentityNotFound) {
		return nil, err
	}

	identity, err = age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("generate AGE identity: %w", err)
	}
	if err := s.store(identity.String()); err != nil {
		if errors.Is(err, errIdentityAlreadyExists) {
			// Another process won the exclusive file publication race. Its key
			// is authoritative; return it instead of rotating the identity.
			return s.Identity()
		}
		return nil, err
	}
	return identity, nil
}

// Identity retrieves the existing local identity without creating one.
func (s *IdentityStore) Identity() (*age.X25519Identity, error) {
	// Once a fallback file exists it is authoritative. This prevents a
	// temporarily unavailable keyring from splitting builds across two keys
	// when the keyring becomes reachable again later.
	identity, fileErr := s.identityFromFile()
	if fileErr == nil {
		return identity, nil
	}
	if !errors.Is(fileErr, ErrIdentityNotFound) {
		return nil, fileErr
	}
	if s.useKeyring && s.keyring != nil {
		value, err := s.keyring.Get(keyringService, keyringUser)
		switch {
		case err == nil:
			return parseIdentity(value, "OS keyring")
		case errors.Is(err, keyring.ErrNotFound):
			return nil, ErrIdentityNotFound
		default:
			// Generate into the fallback on an unavailable/locked keyring.
			// Do not try Set after a failed Get: Set might recover and replace
			// an identity that Get temporarily could not retrieve.
			s.useKeyring = false
			return nil, ErrIdentityNotFound
		}
	}
	return nil, ErrIdentityNotFound
}

// Recipient returns the public recipient for the local identity, generating
// and storing an identity first if necessary.
func (s *IdentityStore) Recipient() (string, error) {
	identity, err := s.EnsureIdentity()
	if err != nil {
		return "", err
	}
	return identity.Recipient().String(), nil
}

func (s *IdentityStore) store(value string) error {
	if s.useKeyring && s.keyring != nil {
		if err := s.keyring.Set(keyringService, keyringUser, value); err == nil {
			return nil
		}
	}
	return s.storeToFile(value)
}

func (s *IdentityStore) storeToFile(value string) error {
	if strings.TrimSpace(s.path) == "" {
		return errors.New("AGE identity fallback path is empty")
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create AGE identity directory: %w", err)
	}
	// MkdirAll and WriteFile retain permissions of existing paths. Tighten
	// both explicitly so a legacy or pre-created fallback cannot stay broad.
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("secure AGE identity directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".builder-age-identity-*")
	if err != nil {
		return fmt.Errorf("create temporary AGE identity file: %w", err)
	}
	tmpPath := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := f.Chmod(0600); err != nil {
		return fmt.Errorf("secure AGE identity file: %w", err)
	}
	if _, err := io.WriteString(f, strings.TrimSpace(value)+"\n"); err != nil {
		return fmt.Errorf("write AGE identity file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync AGE identity file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close AGE identity file: %w", err)
	}
	// Link publishes the complete file atomically and, unlike Rename, fails
	// when any file (including a symlink) already occupies the destination.
	// This is the portable no-replace primitive needed to prevent concurrent
	// first-run processes from silently rotating the identity.
	if err := os.Link(tmpPath, s.path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errIdentityAlreadyExists
		}
		return fmt.Errorf("publish AGE identity file exclusively: %w", err)
	}
	return nil
}

func (s *IdentityStore) identityFromFile() (*age.X25519Identity, error) {
	if strings.TrimSpace(s.path) == "" {
		return nil, ErrIdentityNotFound
	}
	info, err := os.Lstat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrIdentityNotFound
		}
		return nil, fmt.Errorf("inspect AGE identity file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing AGE identity symlink: %s", s.path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("AGE identity path is not a regular file: %s", s.path)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read AGE identity file: %w", err)
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0600 {
			if err := os.Chmod(s.path, 0600); err != nil {
				return nil, fmt.Errorf("secure AGE identity file: %w", err)
			}
		}
	}
	return parseIdentity(string(data), s.path)
}

func parseIdentity(value, source string) (*age.X25519Identity, error) {
	identity, err := age.ParseX25519Identity(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("parse AGE identity from %s: %w", source, err)
	}
	return identity, nil
}

// EnsureIdentity creates the platform-default identity when necessary and
// returns its public recipient. The private identity is never returned as a
// string, which helps keep it out of logs and configuration.
func EnsureIdentity() (string, error) {
	store, err := NewIdentityStore()
	if err != nil {
		return "", err
	}
	return store.Recipient()
}

// Recipient returns the public recipient for the platform-default identity,
// creating the identity if necessary. It is an alias for EnsureIdentity.
func Recipient() (string, error) {
	return EnsureIdentity()
}

// LoadIdentity loads the platform-default private identity without creating a
// replacement. A missing identity is an error because ciphertext encrypted for
// a previous identity cannot be recovered with a newly generated key.
func LoadIdentity() (age.Identity, error) {
	store, err := NewIdentityStore()
	if err != nil {
		return nil, err
	}
	return store.Identity()
}

// Encrypt encrypts plaintext for an AGE X25519 recipient.
func Encrypt(recipient string, plaintext []byte) ([]byte, error) {
	parsed, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return nil, fmt.Errorf("parse AGE recipient: %w", err)
	}
	var ciphertext bytes.Buffer
	w, err := age.Encrypt(&ciphertext, parsed)
	if err != nil {
		return nil, fmt.Errorf("initialize AGE encryption: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("encrypt data: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("finalize AGE encryption: %w", err)
	}
	return ciphertext.Bytes(), nil
}

// DecryptWithIdentity decrypts ciphertext with the supplied AGE identity.
func DecryptWithIdentity(identity age.Identity, ciphertext []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, fmt.Errorf("initialize AGE decryption: %w", err)
	}
	plaintext, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("decrypt data: %w", err)
	}
	return plaintext, nil
}

// DecryptBytes decrypts ciphertext with the platform-default local identity.
func DecryptBytes(ciphertext []byte) ([]byte, error) {
	identity, err := LoadIdentity()
	if err != nil {
		return nil, err
	}
	return DecryptWithIdentity(identity, ciphertext)
}

// Decrypt is retained as a concise alias for DecryptBytes.
func Decrypt(ciphertext []byte) ([]byte, error) { return DecryptBytes(ciphertext) }

// EncryptFile streams sourcePath into an AGE file encrypted for recipient. The
// destination is replaced atomically only after encryption is finalized.
func EncryptFile(recipient, sourcePath, destinationPath string) error {
	parsed, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return fmt.Errorf("parse AGE recipient: %w", err)
	}
	return transformFile(sourcePath, destinationPath, func(dst io.Writer, src io.Reader) error {
		w, err := age.Encrypt(dst, parsed)
		if err != nil {
			return fmt.Errorf("initialize AGE encryption: %w", err)
		}
		if _, err := io.Copy(w, src); err != nil {
			return fmt.Errorf("encrypt file: %w", err)
		}
		if err := w.Close(); err != nil {
			return fmt.Errorf("finalize AGE encryption: %w", err)
		}
		return nil
	})
}

// DecryptFile streams an AGE file through the platform-default identity. The
// plaintext destination is mode 0600 and appears only after authentication and
// decryption complete successfully.
func DecryptFile(sourcePath, destinationPath string) error {
	identity, err := LoadIdentity()
	if err != nil {
		return err
	}
	return DecryptFileWithIdentity(identity, sourcePath, destinationPath)
}

// DecryptFileWithIdentity decrypts a file with an explicitly supplied AGE
// identity. It is useful to callers with a scoped or ephemeral identity.
func DecryptFileWithIdentity(identity age.Identity, sourcePath, destinationPath string) error {
	if identity == nil {
		return errors.New("AGE identity is nil")
	}
	return transformFile(sourcePath, destinationPath, func(dst io.Writer, src io.Reader) error {
		r, err := age.Decrypt(src, identity)
		if err != nil {
			return fmt.Errorf("initialize AGE decryption: %w", err)
		}
		if _, err := io.Copy(dst, r); err != nil {
			return fmt.Errorf("decrypt file: %w", err)
		}
		return nil
	})
}

func transformFile(sourcePath, destinationPath string, transform func(io.Writer, io.Reader) error) (retErr error) {
	src, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer src.Close()

	destinationDir := filepath.Dir(destinationPath)
	if err := os.MkdirAll(destinationDir, 0755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	tmp, err := os.CreateTemp(destinationDir, ".builder-age-*")
	if err != nil {
		return fmt.Errorf("create temporary destination: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return fmt.Errorf("secure temporary destination: %w", err)
	}
	if err := transform(tmp, src); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary destination: %w", err)
	}
	if err := os.Rename(tmpPath, destinationPath); err != nil {
		return fmt.Errorf("publish transformed file: %w", err)
	}
	return nil
}
