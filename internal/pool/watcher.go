package pool

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Credits arrive as files: a user drops a new credential into the auth
// directory, or deletes one they no longer want used. Requiring a restart for
// that is needless friction, so the directory is watched and the pool follows
// the filesystem.

// WatchInterval is how often the credential directories are rescanned. Polling
// rather than inotify keeps the gateway free of a filesystem-notification
// dependency and behaves the same across platforms, including the network
// shares users sometimes keep credentials on.
const WatchInterval = 10 * time.Second

// Watcher keeps the account pool in step with the credential directories.
type Watcher struct {
	pool *AccountPool
	dirs []string

	mu      sync.Mutex
	digests map[string]string // file path -> content digest

	interval time.Duration
	stop     chan struct{}
	stopOnce sync.Once
}

// NewWatcher builds a watcher for the given directories.
func NewWatcher(pool *AccountPool, dirs []string) *Watcher {
	return &Watcher{
		pool:     pool,
		dirs:     dirs,
		digests:  make(map[string]string),
		interval: WatchInterval,
		stop:     make(chan struct{}),
	}
}

// Start begins watching and takes an initial snapshot of the directories.
func (w *Watcher) Start() {
	// Record what is already loaded so the first tick does not treat every
	// existing file as newly added.
	w.snapshot()

	go func() {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				w.Scan()
			case <-w.stop:
				return
			}
		}
	}()
}

// Stop ends the watch loop. Safe to call more than once.
func (w *Watcher) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
}

// snapshot records the current digest of every credential file.
func (w *Watcher) snapshot() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, path := range w.credentialFilesLocked() {
		if digest, err := digestOf(path); err == nil {
			w.digests[path] = digest
		}
	}
}

// Scan reconciles the pool with the filesystem, applying additions, changes and
// removals. It returns the number of accounts added or updated.
func (w *Watcher) Scan() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	current := make(map[string]string)
	changed := 0

	for _, path := range w.credentialFilesLocked() {
		digest, err := digestOf(path)
		if err != nil {
			continue
		}
		current[path] = digest

		// An unchanged file holds nothing new to apply.
		if prev, ok := w.digests[path]; ok && prev == digest {
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		acc, err := ParseAccountData(path, data)
		if err != nil {
			// Report the encrypted case specifically: it is the one where the
			// file looks like a credential but cannot be used, and silence
			// would leave the user wondering why it was ignored.
			if strings.Contains(err.Error(), ErrEncryptedCredential.Error()) {
				log.Printf("[Watcher] 跳过加密凭据 %s: %v", filepath.Base(path), err)
			}
			continue
		}
		if acc == nil {
			continue
		}

		w.pool.AddOrUpdate(acc)
		changed++
		log.Printf("[Watcher] 已热加载账号 %s (%s)", acc.Name, filepath.Base(path))
	}

	// A file that vanished means the credential was withdrawn, so its account
	// must leave the pool rather than keep serving traffic.
	for path := range w.digests {
		if _, stillThere := current[path]; stillThere {
			continue
		}
		for _, acc := range w.pool.List() {
			if acc.FilePath == path {
				w.pool.Remove(acc.ID)
				log.Printf("[Watcher] 凭据已移除，账号下线: %s", acc.Name)
			}
		}
	}

	w.digests = current
	return changed
}

// credentialFilesLocked lists the credential files across every watched
// directory. The caller holds the lock.
func (w *Watcher) credentialFilesLocked() []string {
	var out []string
	for _, dir := range w.dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.ToLower(e.Name())
			if strings.Contains(name, ".sample.") || strings.HasPrefix(name, "sample") {
				continue
			}
			// A temp file from an in-progress atomic write is not a credential.
			if strings.Contains(name, ".tmp-") {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".json" && ext != ".info" {
				continue
			}
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// digestOf returns a content digest for change detection.
func digestOf(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
