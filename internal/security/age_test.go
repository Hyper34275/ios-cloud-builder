package security

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
)

type fakeKeyring struct {
	value  string
	getErr error
	setErr error
}

func (f *fakeKeyring) Get(_, _ string) (string, error) { return f.value, f.getErr }
func (f *fakeKeyring) Set(_, _, value string) error {
	if f.setErr == nil {
		f.value = value
		f.getErr = nil
	}
	return f.setErr
}

func TestIdentityStoreGeneratePersistAndEncryptRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "age-identity")
	store := NewFileIdentityStore(path)

	recipient, err := store.Recipient()
	if err != nil {
		t.Fatalf("Recipient() error = %v", err)
	}
	identity, err := store.Identity()
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}
	if got := identity.Recipient().String(); got != recipient {
		t.Fatalf("stored recipient = %q, want %q", got, recipient)
	}

	plaintext := []byte("private build output\ncompiler detail")
	ciphertext, err := Encrypt(recipient, plaintext)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if bytes.Contains(ciphertext, plaintext) {
		t.Fatal("ciphertext contains plaintext")
	}
	decrypted, err := DecryptWithIdentity(identity, ciphertext)
	if err != nil {
		t.Fatalf("DecryptWithIdentity() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestIdentityStoreFallbackPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX permission bits")
	}
	path := filepath.Join(t.TempDir(), "identity-dir", "identity")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	store := NewFileIdentityStore(path)
	if _, err := store.Recipient(); err == nil {
		t.Fatal("Recipient() unexpectedly accepted invalid pre-existing identity")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Recipient(); err != nil {
		t.Fatalf("Recipient() error = %v", err)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Errorf("identity permissions = %04o, want 0600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Errorf("identity directory permissions = %04o, want 0700", got)
	}
}

func TestIdentityNotFound(t *testing.T) {
	store := NewFileIdentityStore(filepath.Join(t.TempDir(), "missing"))
	_, err := store.Identity()
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("Identity() error = %v, want ErrIdentityNotFound", err)
	}
}

func TestIdentityStoreUsesKeyringWhenAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fallback")
	kr := &fakeKeyring{getErr: keyring.ErrNotFound}
	store := &IdentityStore{path: path, keyring: kr, useKeyring: true}
	recipient, err := store.Recipient()
	if err != nil {
		t.Fatalf("Recipient() error = %v", err)
	}
	if kr.value == "" {
		t.Fatal("identity was not stored in keyring")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fallback file exists after successful keyring write: %v", err)
	}
	loaded, err := store.Identity()
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}
	if got := loaded.Recipient().String(); got != recipient {
		t.Errorf("recipient = %q, want %q", got, recipient)
	}
}

func TestIdentityStoreFallsBackWhenKeyringWriteFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fallback", "identity")
	kr := &fakeKeyring{getErr: keyring.ErrNotFound, setErr: errors.New("keyring unavailable")}
	store := &IdentityStore{path: path, keyring: kr, useKeyring: true}
	if _, err := store.Recipient(); err != nil {
		t.Fatalf("Recipient() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("fallback identity was not created: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Errorf("fallback permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func TestEncryptRejectsInvalidRecipient(t *testing.T) {
	if _, err := Encrypt("not-an-age-recipient", []byte("secret")); err == nil {
		t.Fatal("Encrypt() unexpectedly accepted invalid recipient")
	}
}

func TestEncryptDecryptFileRoundTrip(t *testing.T) {
	identityPath := filepath.Join(t.TempDir(), "identity")
	store := NewFileIdentityStore(identityPath)
	identity, err := store.EnsureIdentity()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "App.ipa")
	encrypted := filepath.Join(dir, "artifact", "App.ipa.age")
	decrypted := filepath.Join(dir, "dist", "App.ipa")
	want := bytes.Repeat([]byte("private-ipa-data\x00"), 8192)
	if err := os.WriteFile(source, want, 0644); err != nil {
		t.Fatal(err)
	}
	if err := EncryptFile(identity.Recipient().String(), source, encrypted); err != nil {
		t.Fatalf("EncryptFile() error = %v", err)
	}
	if err := DecryptFileWithIdentity(identity, encrypted, decrypted); err != nil {
		t.Fatalf("DecryptFileWithIdentity() error = %v", err)
	}
	got, err := os.ReadFile(decrypted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decrypted file does not match source")
	}
}

func TestConcurrentEnsureIdentityDoesNotRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity")
	const callers = 24
	recipients := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recipient, err := NewFileIdentityStore(path).Recipient()
			if err != nil {
				errs <- err
				return
			}
			recipients <- recipient
		}()
	}
	wg.Wait()
	close(errs)
	close(recipients)
	for err := range errs {
		t.Errorf("concurrent Recipient() error = %v", err)
	}
	var want string
	for recipient := range recipients {
		if want == "" {
			want = recipient
		}
		if recipient != want {
			t.Errorf("recipient rotated: got %q, want %q", recipient, want)
		}
	}
	if want == "" {
		t.Fatal("no recipient returned")
	}
}

func TestIdentityStoreRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	identity, err := NewFileIdentityStore(target).EnsureIdentity()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "identity-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	store := NewFileIdentityStore(link)
	if _, err := store.Identity(); err == nil {
		t.Fatal("Identity() unexpectedly followed symlink")
	}
	if _, err := store.EnsureIdentity(); err == nil {
		t.Fatal("EnsureIdentity() unexpectedly replaced/followed symlink")
	}
	loaded, err := NewFileIdentityStore(target).Identity()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.String() != identity.String() {
		t.Fatal("target identity changed")
	}
}
