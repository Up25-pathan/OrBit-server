package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/orbit/control-server/internal/config"
)

// fakeS3 is a minimal in-memory S3-compatible endpoint covering the operations
// the backup sink uses (PUT object, GET object, NoSuchKey 404).
func fakeS3(t *testing.T) (*httptest.Server, *sync.Map) {
	t.Helper()
	objects := &sync.Map{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/orbitbucket/")
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			objects.Store(key, body)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if v, ok := objects.Load(key); ok {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(v.([]byte))
			} else {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message><RequestId>x</RequestId><HostId>y</HostId></Error>`))
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, objects
}

func backupCfg(t *testing.T, srv *httptest.Server) config.BackupConfig {
	t.Helper()
	return config.BackupConfig{
		Enabled:   true,
		Endpoint:  srv.URL,
		Bucket:    "orbitbucket",
		AccessKey: "test-access-key",
		SecretKey: "test-secret-key",
		Region:    "auto",
	}
}

func TestBackupUploadDownloadRoundTrip(t *testing.T) {
	srv, objects := fakeS3(t)
	sink, err := NewBackupSink(backupCfg(t, srv))
	if err != nil {
		t.Fatalf("NewBackupSink: %v", err)
	}

	ctx := context.Background()
	payload := []byte(`{"users":{},"friends":{},"projects":{}}`)
	if err := sink.Upload(ctx, backupDBKey, payload); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	data, found, err := sink.Download(ctx, backupDBKey)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !found {
		t.Fatal("expected object to be found after upload")
	}
	if string(data) != string(payload) {
		t.Fatalf("round-trip mismatch: got %q want %q", data, payload)
	}

	// Missing key must report found=false, not an error.
	_, found, err = sink.Download(ctx, backupSaltKey)
	if err != nil {
		t.Fatalf("Download missing: %v", err)
	}
	if found {
		t.Fatal("missing key should report found=false")
	}

	if _, ok := objects.Load(backupDBKey); !ok {
		t.Fatal("uploaded object missing from fake store")
	}
}

func TestUploadDBMirrorsSecrets(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "orbit.db")
	if err := os.WriteFile(filepath.Join(dir, "orbit.jwt-secret"), []byte("jwt-secret-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orbit.invite-salt"), []byte("salt-value\n"), 0600); err != nil {
		t.Fatal(err)
	}

	srv, objects := fakeS3(t)
	sink, err := NewBackupSink(backupCfg(t, srv))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := sink.UploadDB(ctx, dbPath, []byte(`{"users":{}}`)); err != nil {
		t.Fatalf("UploadDB: %v", err)
	}

	for _, key := range []string{backupDBKey, backupJWTKey, backupSaltKey} {
		if _, ok := objects.Load(key); !ok {
			t.Fatalf("expected %s to be uploaded", key)
		}
	}
}

func TestRestoreBackupFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "orbit.db")

	srv, _ := fakeS3(t)
	sink, err := NewBackupSink(backupCfg(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dbJSON := []byte(`{"users":{"u1":{}},"friends":{"u1":["u2"]},"projects":{"p1":{}}}`)
	if err := sink.Upload(ctx, backupDBKey, dbJSON); err != nil {
		t.Fatal(err)
	}
	if err := sink.Upload(ctx, backupJWTKey, []byte("persisted-jwt\n")); err != nil {
		t.Fatal(err)
	}

	if err := RestoreBackupFiles(dbPath, backupCfg(t, srv)); err != nil {
		t.Fatalf("RestoreBackupFiles: %v", err)
	}

	got, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("restored db not written: %v", err)
	}
	if string(got) != string(dbJSON) {
		t.Fatalf("restored db mismatch: got %q want %q", got, dbJSON)
	}
	jwt, err := os.ReadFile(filepath.Join(dir, "orbit.jwt-secret"))
	if err != nil {
		t.Fatalf("restored jwt secret not written: %v", err)
	}
	if string(jwt) != "persisted-jwt\n" {
		t.Fatalf("restored jwt mismatch: %q", jwt)
	}
}

func TestRestoreBackupFilesKeepsExistingLocalData(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "orbit.db")

	srv, _ := fakeS3(t)
	ctx := context.Background()
	sink, err := NewBackupSink(backupCfg(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Upload(ctx, backupDBKey, []byte(`{"users":{"remote":{}}}`)); err != nil {
		t.Fatal(err)
	}

	// A local non-empty DB must be trusted over the remote backup.
	local := []byte(`{"users":{"local":{}}}`)
	if err := os.WriteFile(dbPath, local, 0600); err != nil {
		t.Fatal(err)
	}

	if err := RestoreBackupFiles(dbPath, backupCfg(t, srv)); err != nil {
		t.Fatalf("RestoreBackupFiles: %v", err)
	}

	got, _ := os.ReadFile(dbPath)
	if string(got) != string(local) {
		t.Fatalf("local data was clobbered by backup: got %q want %q", got, local)
	}
}

func TestRestoreBackupFilesDisabledIsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := RestoreBackupFiles(filepath.Join(dir, "orbit.db"), config.BackupConfig{}); err != nil {
		t.Fatalf("disabled restore should be a no-op: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "orbit.db")); !os.IsNotExist(err) {
		t.Fatal("disabled restore must not create files")
	}
}
