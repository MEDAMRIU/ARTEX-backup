package server

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestJWTKeyConcurrentCreationAndPersistence(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	keys := make(chan []byte, 12)
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); key, err := loadOrCreateJWTKey(keyDir, dataDir); keys <- key; errs <- err }()
	}
	wg.Wait()
	close(keys)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first []byte
	for key := range keys {
		if first == nil {
			first = key
		}
		if !bytes.Equal(key, first) {
			t.Fatal("concurrent key mismatch")
		}
	}
	again, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil || !bytes.Equal(again, first) {
		t.Fatal("key did not persist", err)
	}
}

func TestJWTKeyMigrationAndInvalidKeyFailsClosed(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	legacy := filepath.Join(dataDir, jwtKeyFilename)
	original := bytes.Repeat([]byte("a"), 32)
	if err := os.WriteFile(legacy, original, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("migration failed", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy key remains browsable")
	}
	path := filepath.Join(keyDir, jwtKeyFilename)
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateJWTKey(keyDir, dataDir); err == nil {
		t.Fatal("invalid existing key silently replaced")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "corrupt" {
		t.Fatal("existing key overwritten")
	}
}
