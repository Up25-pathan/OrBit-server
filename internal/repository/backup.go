package repository

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	s3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/orbit/control-server/internal/config"
)

// Object keys inside the backup bucket. Keys are fixed regardless of the local
// database path so a redeploy with a different working directory still finds them.
const (
	backupDBKey   = "orbit/backup/orbit.db"
	backupJWTKey  = "orbit/backup/orbit.jwt-secret"
	backupSaltKey = "orbit/backup/orbit.invite-salt"
)

// BackupSink uploads/downloads the database and persisted secrets to any
// S3-compatible object store (Cloudflare R2 by default). The server keeps
// running against its local JSON file; the bucket is purely a durable mirror.
type BackupSink struct {
	client *s3.Client
	bucket string
}

func NewBackupSink(cfg config.BackupConfig) (*BackupSink, error) {
	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
	client := s3.New(s3.Options{
		Region:        cfg.Region,
		Credentials:   creds,
		BaseEndpoint:  aws.String(cfg.Endpoint),
		UsePathStyle:  true,
	})
	return &BackupSink{client: client, bucket: cfg.Bucket}, nil
}

func (b *BackupSink) Upload(ctx context.Context, key string, data []byte) error {
	_, err := b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	return err
}

func (b *BackupSink) Download(ctx context.Context, key string) ([]byte, bool, error) {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		var notFound *types.NotFound
		if errors.As(err, &noSuchKey) || errors.As(err, &notFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// UploadDB uploads the marshalled database plus the persisted secret files.
func (b *BackupSink) UploadDB(ctx context.Context, dbPath string, data []byte) error {
	if err := b.Upload(ctx, backupDBKey, data); err != nil {
		return err
	}
	for _, pair := range []struct{ file, key string }{
		{secretFilePath(dbPath, "orbit.jwt-secret"), backupJWTKey},
		{secretFilePath(dbPath, "orbit.invite-salt"), backupSaltKey},
	} {
		secretBytes, err := os.ReadFile(pair.file)
		if err != nil {
			continue
		}
		if err := b.Upload(ctx, pair.key, secretBytes); err != nil {
			log.Printf("[Backup] secret upload failed for %s: %v", pair.key, err)
		}
	}
	return nil
}

// secretFilePath mirrors config.secretFilePath: secrets live next to the database.
func secretFilePath(dbPath, fileName string) string {
	dir := filepath.Dir(dbPath)
	if dir == "." {
		return fileName
	}
	return filepath.Join(dir, fileName)
}

// RestoreBackupFiles runs at startup, BEFORE config.Load() generates secrets.
// For each of the database and secret files: if a local non-empty copy exists it
// is trusted; otherwise the durable copy is pulled down from the bucket and
// written back, so a wiped ephemeral filesystem (Render restart/redeploy) is
// healed without losing friendships, memberships or relayed deltas.
func RestoreBackupFiles(dbPath string, cfg config.BackupConfig) error {
	if !cfg.Enabled {
		return nil
	}
	sink, err := NewBackupSink(cfg)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	files := []struct{ local, key string }{
		{dbPath, backupDBKey},
		{secretFilePath(dbPath, "orbit.jwt-secret"), backupJWTKey},
		{secretFilePath(dbPath, "orbit.invite-salt"), backupSaltKey},
	}

	restored := 0
	for _, f := range files {
		if localFilePresent(f.local) {
			continue
		}
		data, found, err := sink.Download(ctx, f.key)
		if err != nil {
			log.Printf("[Backup] restore check failed for %s: %v", f.key, err)
			continue
		}
		if !found {
			continue
		}
		if dir := filepath.Dir(f.local); dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				log.Printf("[Backup] failed to create dir for %s: %v", f.local, err)
				continue
			}
		}
		if err := os.WriteFile(f.local, data, 0600); err != nil {
			log.Printf("[Backup] failed to write restored %s: %v", f.local, err)
			continue
		}
		restored++
		log.Printf("[Backup] restored %s from durable backup", f.local)
	}
	if restored > 0 {
		log.Printf("[Backup] restore complete: %d file(s) recovered", restored)
	}
	return nil
}

func localFilePresent(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() > 0
}
