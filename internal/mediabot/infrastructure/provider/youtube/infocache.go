package youtube

import (
	"os"
	"sync"
	"time"
)

// One user request used to cost two yt-dlp extractions — FetchInfo, then
// Download — and from a datacenter IP the *second* one is what YouTube answers
// with `HTTP Error 403: Forbidden` on the Innertube API, leaving the download
// with no formats to choose from. FetchInfo therefore keeps the extraction it
// already paid for (`--write-info-json`) and Download replays it
// (`--load-info-json`), which skips extraction completely: no webpage fetch, no
// API page, no 403.
//
// The cache is deliberately internal to this adapter. Handing the info file to
// the application layer would leak a yt-dlp implementation detail through
// domain.Provider for no gain.

// infoTTL bounds how long a stored extraction stays usable. The signed media
// URLs inside it live for hours, so this is about the queue: a download that
// starts long after its metadata was fetched should extract afresh rather than
// replay something stale.
const infoTTL = 15 * time.Minute

// infoCacheMax caps entries so an abandoned queue cannot grow the map without
// bound. Each entry is one small temp directory.
const infoCacheMax = 64

type infoEntry struct {
	dir  string // temp dir holding info.info.json; removed when consumed
	path string // the JSON itself
	at   time.Time
}

type infoCache struct {
	mu      sync.Mutex
	entries map[string]infoEntry // keyed by URL
	now     func() time.Time     // injectable for tests
}

func newInfoCache() *infoCache {
	return &infoCache{entries: make(map[string]infoEntry), now: time.Now}
}

// put stores the extraction for url, evicting anything expired first. An
// existing entry for the same URL is replaced and its directory removed.
func (c *infoCache) put(url, dir, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sweepLocked()
	if old, ok := c.entries[url]; ok {
		_ = os.RemoveAll(old.dir)
	}
	if len(c.entries) >= infoCacheMax {
		c.dropOldestLocked()
	}
	c.entries[url] = infoEntry{dir: dir, path: path, at: c.now()}
}

// take returns the stored JSON path for url and forgets it. The caller owns the
// directory from then on and must call Cleanup when the download is done.
// An empty result means "extract normally".
func (c *infoCache) take(url string) (path, dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[url]
	if !ok {
		return "", ""
	}
	delete(c.entries, url)
	if c.now().Sub(e.at) > infoTTL {
		_ = os.RemoveAll(e.dir)
		return "", ""
	}
	if _, err := os.Stat(e.path); err != nil {
		_ = os.RemoveAll(e.dir)
		return "", ""
	}
	return e.path, e.dir
}

// sweepLocked removes expired entries. Called on put, which is the only moment
// the map grows — a background goroutine would be more machinery than a cache
// this small deserves.
func (c *infoCache) sweepLocked() {
	for url, e := range c.entries {
		if c.now().Sub(e.at) > infoTTL {
			_ = os.RemoveAll(e.dir)
			delete(c.entries, url)
		}
	}
}

func (c *infoCache) dropOldestLocked() {
	var oldestURL string
	var oldest time.Time
	for url, e := range c.entries {
		if oldestURL == "" || e.at.Before(oldest) {
			oldestURL, oldest = url, e.at
		}
	}
	if oldestURL != "" {
		_ = os.RemoveAll(c.entries[oldestURL].dir)
		delete(c.entries, oldestURL)
	}
}
