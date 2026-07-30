package cookiestore

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeS3 serves just enough of the S3 API for GetObject and PutObject against
// one key. Path-style addressing means requests arrive as /bucket/key.
type fakeS3 struct {
	mu sync.Mutex
	// object is nil while the bucket is empty, which is what a fresh bucket
	// looks like on first boot.
	object []byte
	puts   [][]byte
}

func (f *fakeS3) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			if f.object == nil {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
					`<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
				return
			}
			_, _ = w.Write(f.object)
		case http.MethodPut:
			body := make([]byte, r.ContentLength)
			if _, err := r.Body.Read(body); err != nil && err.Error() != "EOF" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			f.object = body
			f.puts = append(f.puts, body)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func (f *fakeS3) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.puts)
}

func (f *fakeS3) setObject(b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.object = b
}

func newTestStore(t *testing.T, fake *fakeS3) (*Store, string) {
	t.Helper()
	// Static creds: the SDK signs every request, and New() reads them from the
	// environment exactly as `fly storage create` provides them.
	t.Setenv("AWS_ACCESS_KEY_ID", "tid_test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "tsec_test")

	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "yt-cookies.txt")
	store, err := New(context.Background(), Config{
		Bucket:   "cookies",
		Key:      "yt-cookies.txt",
		Endpoint: srv.URL,
		Region:   "auto",
		Path:     path,
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, path
}

func TestLoad_WritesBucketCopyWith0600(t *testing.T) {
	fake := &fakeS3{}
	fake.setObject([]byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tFALSE\t0\tSID\tfresh\n"))
	store, path := newTestStore(t, fake)

	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cookie file: %v", err)
	}
	if string(got) != string(fake.object) {
		t.Errorf("cookie file = %q, want the bucket copy", got)
	}
	// Cookies are credentials — the file must not be group/world readable.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

func TestLoad_MissingObjectReportsErrNoObjectAndKeepsSeed(t *testing.T) {
	fake := &fakeS3{} // empty bucket
	store, path := newTestStore(t, fake)

	seed := []byte("seed-from-secret\n")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}

	err := store.Load(context.Background())
	if !errors.Is(err, ErrNoObject) {
		t.Fatalf("Load on empty bucket: err = %v, want ErrNoObject", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(seed) {
		t.Errorf("seed file was modified: %q", got)
	}
}

func TestSave_UploadsLocalFile(t *testing.T) {
	fake := &fakeS3{}
	store, path := newTestStore(t, fake)

	body := []byte("rotated-cookies\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if fake.putCount() != 1 {
		t.Fatalf("puts = %d, want 1", fake.putCount())
	}
	if string(fake.object) != string(body) {
		t.Errorf("uploaded %q, want %q", fake.object, body)
	}
}

// The mirror must not re-upload what it just downloaded, otherwise every boot
// writes the object back for no reason.
func TestSaveIfChanged_SkipsUnchangedFileAndUploadsRotations(t *testing.T) {
	fake := &fakeS3{}
	fake.setObject([]byte("original\n"))
	store, path := newTestStore(t, fake)

	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	store.saveIfChanged(context.Background())
	if fake.putCount() != 0 {
		t.Fatalf("unchanged file uploaded %d times, want 0", fake.putCount())
	}

	// Simulate yt-dlp rewriting the jar. The mtime must move for the change to
	// be visible, and some filesystems have coarse timestamps.
	if err := os.WriteFile(path, []byte("rotated-by-ytdlp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	store.saveIfChanged(context.Background())
	if fake.putCount() != 1 {
		t.Fatalf("rotation uploaded %d times, want 1", fake.putCount())
	}
	if string(fake.object) != "rotated-by-ytdlp\n" {
		t.Errorf("bucket holds %q, want the rotated jar", fake.object)
	}
}

func TestNew_RequiresBucketAndPath(t *testing.T) {
	if _, err := New(context.Background(), Config{Path: "/tmp/x"}, nil); err == nil {
		t.Error("missing bucket accepted")
	}
	if _, err := New(context.Background(), Config{Bucket: "b"}, nil); err == nil {
		t.Error("missing path accepted")
	}
}
