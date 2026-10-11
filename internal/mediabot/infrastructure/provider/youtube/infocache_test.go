package youtube

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// stageInfo creates a temp dir holding a stub info JSON, mimicking what
// FetchInfo leaves behind.
func stageInfo(t *testing.T) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "info.info.json")
	if err := os.WriteFile(path, []byte(`{"id":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestInfoCache_TakeReturnsStoredExtractionOnce(t *testing.T) {
	c := newInfoCache()
	dir, path := stageInfo(t)
	c.put("https://youtu.be/x", dir, path)

	gotPath, gotDir := c.take("https://youtu.be/x")
	if gotPath != path || gotDir != dir {
		t.Fatalf("take = (%q, %q), want (%q, %q)", gotPath, gotDir, path, dir)
	}

	// Consumed: a second download for the same URL must extract normally.
	if again, _ := c.take("https://youtu.be/x"); again != "" {
		t.Errorf("second take = %q, want empty", again)
	}
}

func TestInfoCache_UnknownURL(t *testing.T) {
	c := newInfoCache()
	if got, _ := c.take("https://youtu.be/never-stored"); got != "" {
		t.Errorf("take = %q, want empty", got)
	}
}

func TestInfoCache_ExpiredEntryIsDiscarded(t *testing.T) {
	c := newInfoCache()
	dir, path := stageInfo(t)
	c.put("https://youtu.be/x", dir, path)

	// Jump past the TTL: a stale extraction must not be replayed.
	c.now = func() time.Time { return time.Now().Add(infoTTL + time.Minute) }

	if got, _ := c.take("https://youtu.be/x"); got != "" {
		t.Errorf("take of expired entry = %q, want empty", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expired temp dir not removed (err=%v)", err)
	}
}

func TestInfoCache_MissingFileIsDiscarded(t *testing.T) {
	c := newInfoCache()
	dir, path := stageInfo(t)
	c.put("https://youtu.be/x", dir, path)

	// tmpfs cleared under us, or the file was removed by something else.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.take("https://youtu.be/x"); got != "" {
		t.Errorf("take with missing JSON = %q, want empty", got)
	}
}

func TestInfoCache_PutReplacesAndCleansUpPrevious(t *testing.T) {
	c := newInfoCache()
	oldDir, oldPath := stageInfo(t)
	c.put("https://youtu.be/x", oldDir, oldPath)

	newDir, newPath := stageInfo(t)
	c.put("https://youtu.be/x", newDir, newPath)

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Errorf("replaced temp dir not removed (err=%v)", err)
	}
	if got, _ := c.take("https://youtu.be/x"); got != newPath {
		t.Errorf("take = %q, want the newer path", got)
	}
}

func TestInfoCache_EvictsWhenFull(t *testing.T) {
	c := newInfoCache()
	base := time.Now()
	for i := 0; i < infoCacheMax+5; i++ {
		dir, path := stageInfo(t)
		// Stagger timestamps so "oldest" is well defined.
		c.now = func() time.Time { return base.Add(time.Duration(i) * time.Second) }
		c.put("https://youtu.be/"+strconv.Itoa(i), dir, path)
	}
	if len(c.entries) > infoCacheMax {
		t.Errorf("entries = %d, want <= %d", len(c.entries), infoCacheMax)
	}
	// The most recent URL must still be there; the first ones must be gone.
	if got, _ := c.take("https://youtu.be/" + strconv.Itoa(infoCacheMax+4)); got == "" {
		t.Error("newest entry was evicted")
	}
	if got, _ := c.take("https://youtu.be/0"); got != "" {
		t.Error("oldest entry survived eviction")
	}
}
