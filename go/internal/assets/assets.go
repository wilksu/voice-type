package assets

import (
	"bytes"
	"embed"
	"os"
	"path/filepath"
)

//go:embed scripts/*.py
var embeddedScripts embed.FS

func UIRootDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "voice-type", "ui")
}

// EnsureExtracted writes embedded Python UI/KWin helper scripts to ~/.local/share/voice-type/ui/
// when missing or updated, and returns the directory path.
func EnsureExtracted() (string, error) {
	dir := UIRootDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	entries, err := embeddedScripts.ReadDir("scripts")
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := embeddedScripts.ReadFile("scripts/" + entry.Name())
		if err != nil {
			return "", err
		}
		dest := filepath.Join(dir, entry.Name())
		existing, err := os.ReadFile(dest)
		if err == nil && bytes.Equal(existing, data) {
			continue
		}
		if err := os.WriteFile(dest, data, 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func ScriptPath(name string) (string, error) {
	dir, err := EnsureExtracted()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}
