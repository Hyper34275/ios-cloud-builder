package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"tilde", "~/Desktop/my-cert.p12", filepath.Join(home, "Desktop", "my-cert.p12")},
		{"bare tilde", "~", home},
		{"trailing whitespace", "  ~/cert.p12 ", filepath.Join(home, "cert.p12")},
		{"double quoted", `"~/My Certs/cert.p12"`, filepath.Join(home, "My Certs", "cert.p12")},
		{"single quoted", `'/tmp/cert.p12'`, "/tmp/cert.p12"},
		{"escaped spaces", `/tmp/My\ Certs/cert.p12`, "/tmp/My Certs/cert.p12"},
		{"absolute unchanged", "/tmp/cert.p12", "/tmp/cert.p12"},
		{"relative unchanged", "certs/cert.p12", "certs/cert.p12"},
		{"other user tilde untouched", "~someone/cert.p12", "~someone/cert.p12"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := expandPath(tt.in); got != tt.want {
				t.Errorf("expandPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
