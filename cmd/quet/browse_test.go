package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/8bu/quet/internal/storage"
)

func TestOpenPickedGate(t *testing.T) {
	dir := t.TempDir()
	// An explicit empty config and flags file keep the test independent of ~/.config.
	cfgPath := filepath.Join(dir, "quet.yaml")
	flagsPath := filepath.Join(dir, "flags.yaml")
	for path, body := range map[string]string{cfgPath: "{}\n", flagsPath: "flags: []\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := command{kind: "review", configPath: cfgPath, hasConfig: true, flagsFile: flagsPath, hasFlagsFile: true}

	tests := []struct {
		name, file, body string
		wantErr          string // "" = opens
	}{
		{"not json", "broken.json", "{not json", "corpus"},
		{"json object that is not a corpus", "package.json", `{"name": "quet", "version": "0.1.0"}`, "no records array"},
		{"no records", "empty.jsonl", "", "no records"},
		{"objects without text", "meta.jsonl", "{\"name\":\"a\"}\n", "no record has text"},
		{"valid corpus", "notes.jsonl", "{\"text\":\"CK Nam 2tr\"}\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.file)
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := openPicked(cmd, path)
			if s != nil {
				defer s.Close()
			}
			_, statErr := os.Stat(storage.SidecarPath(path))
			sidecar := statErr == nil
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("openPicked: %v", err)
				}
				if !sidecar {
					t.Fatal("opened corpus has no sidecar")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("openPicked error = %v, want it to contain %q", err, tt.wantErr)
			}
			if sidecar {
				t.Fatal("rejected file got a sidecar")
			}
		})
	}
}
