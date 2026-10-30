package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// The user's own proxy setting is saved before the core changes it and kept
// until the core restores it, so a killed core can be undone by
// "huanyou-core restore" on the next app start.

func backupPath() string {
	dir, err := os.UserCacheDir() // %LocalAppData% / ~/Library/Caches
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "Huanyou", "proxy-backup.json")
}

func hasBackup() bool {
	_, err := os.Stat(backupPath())
	return !errors.Is(err, os.ErrNotExist)
}

func saveBackup(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(backupPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(backupPath(), b, 0o600)
}

// loadBackup reads the saved setting into v; ok is false when there is none.
// A corrupt file returns ok with err set, so the caller can still turn the
// proxy off.
func loadBackup(v any) (ok bool, err error) {
	b, err := os.ReadFile(backupPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

func removeBackup() error { return os.Remove(backupPath()) }
