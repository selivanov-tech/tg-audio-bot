// Package cookiestore keeps the yt-dlp cookie file alive across restarts by
// mirroring it to S3-compatible object storage (Tigris on Fly).
//
// Why this exists: YouTube rotates account cookies, and yt-dlp writes the
// refreshed jar back to the file it was given. On Fly the file lives on tmpfs
// and the entrypoint re-creates it from the YTDLP_COOKIES secret at every boot,
// so with scale-to-zero every refresh is thrown away and the seed snapshot ages
// until YouTube rejects it ("The provided YouTube account cookies are no longer
// valid"). Loading the file from the bucket at boot and writing it back when it
// changes closes that loop, so the credentials keep themselves current.
package cookiestore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Config is the store's dependency surface. Credentials are not listed: the AWS
// SDK reads AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY from the environment,
// which is exactly how `fly storage create` provisions them.
type Config struct {
	Bucket   string // BUCKET_NAME; empty disables the store
	Key      string // object key, e.g. "yt-cookies.txt"
	Endpoint string // AWS_ENDPOINT_URL_S3, e.g. https://fly.storage.tigris.dev
	Region   string // AWS_REGION; Tigris uses "auto"
	Path     string // local cookie file the bot passes to yt-dlp
}

// Store mirrors one local file to one object.
type Store struct {
	client *s3.Client
	cfg    Config
	log    *slog.Logger

	// lastSynced is the file state already present in the bucket, so an
	// unchanged file is never re-uploaded.
	lastSynced fileState
}

type fileState struct {
	modTime time.Time
	size    int64
}

// ErrNoObject reports that the bucket has no cookie object yet — the caller
// should keep whatever the entrypoint seeded from the secret.
var ErrNoObject = errors.New("cookiestore: object not found")

func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Bucket == "" {
		return nil, errors.New("cookiestore: bucket is required")
	}
	if cfg.Path == "" {
		return nil, errors.New("cookiestore: local path is required")
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("cookiestore: load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = &cfg.Endpoint
		}
		// Path style (endpoint/bucket/key) rather than virtual-host
		// (bucket.endpoint): Tigris accepts both, and it is the only form that
		// works against an endpoint whose host cannot carry a bucket prefix —
		// which is what makes this adapter testable against a local server.
		o.UsePathStyle = true
	})
	return &Store{client: client, cfg: cfg, log: logger}, nil
}

// Load replaces the local file with the bucket's copy. It returns ErrNoObject
// when the bucket has none, leaving the local file untouched.
func (s *Store) Load(ctx context.Context) error {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.cfg.Bucket,
		Key:    &s.cfg.Key,
	})
	if err != nil {
		var missing *types.NoSuchKey
		if errors.As(err, &missing) {
			return ErrNoObject
		}
		// Tigris answers a missing key with 404 NotFound rather than
		// NoSuchKey on some paths; treat both as "nothing stored yet".
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return ErrNoObject
		}
		return fmt.Errorf("cookiestore: get object: %w", err)
	}
	defer func() { _ = out.Body.Close() }()

	// Cookies are credentials: 0600, and write through a temp file so a failed
	// download cannot leave a truncated cookie file behind.
	tmp := s.cfg.Path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("cookiestore: create temp: %w", err)
	}
	if _, err := io.Copy(f, out.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("cookiestore: write temp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cookiestore: close temp: %w", err)
	}
	if err := os.Rename(tmp, s.cfg.Path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cookiestore: rename temp: %w", err)
	}

	// Record what we just wrote so Run does not immediately upload it back.
	s.lastSynced = s.stat()
	return nil
}

// Save uploads the local file to the bucket.
func (s *Store) Save(ctx context.Context) error {
	f, err := os.Open(s.cfg.Path)
	if err != nil {
		return fmt.Errorf("cookiestore: open local: %w", err)
	}
	defer func() { _ = f.Close() }()

	state := s.stat()
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        &s.cfg.Bucket,
		Key:           &s.cfg.Key,
		Body:          f,
		ContentLength: &state.size,
	}); err != nil {
		return fmt.Errorf("cookiestore: put object: %w", err)
	}
	s.lastSynced = state
	return nil
}

// Run uploads the file whenever it changes, and once more on shutdown.
//
// Polling beats hooking the download path: yt-dlp rewrites the jar as a side
// effect of any invocation, so the file — not the application flow — is the
// thing worth watching, and the provider stays free of storage concerns.
func (s *Store) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.saveIfChanged(ctx)
		case <-ctx.Done():
			// Fly sends SIGINT before stopping the machine, which cancels
			// ctx — so the last refresh still needs a live context of its own.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s.saveIfChanged(shutdownCtx)
			return
		}
	}
}

func (s *Store) saveIfChanged(ctx context.Context) {
	state := s.stat()
	if state == (fileState{}) || state == s.lastSynced {
		return
	}
	if err := s.Save(ctx); err != nil {
		s.log.Error("cookie store save failed", "err", err)
		return
	}
	s.log.Info("cookie store saved", "bucket", s.cfg.Bucket, "key", s.cfg.Key, "size", state.size)
}

// stat returns the zero value when the file is missing, which reads as
// "nothing to sync".
func (s *Store) stat() fileState {
	fi, err := os.Stat(s.cfg.Path)
	if err != nil {
		return fileState{}
	}
	return fileState{modTime: fi.ModTime(), size: fi.Size()}
}
