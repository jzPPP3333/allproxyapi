package pool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- atomic writes ---------------------------------------------------------

func TestWriteFileAtomicReplacesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acct.json")

	if err := writeFileAtomic(path, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatalf("first write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("content = %s", got)
	}

	// Overwriting must replace, not merge or append.
	if err := writeFileAtomic(path, []byte(`{"a":2}`), 0o600); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != `{"a":2}` {
		t.Fatalf("content after overwrite = %s", got)
	}
}

// TestWriteFileAtomicLeavesNoTempFiles checks the temp sibling is cleaned up,
// so repeated refreshes do not litter the credential directory.
func TestWriteFileAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acct.json")

	for i := 0; i < 5; i++ {
		if err := writeFileAtomic(path, []byte(`{"a":1}`), 0o600); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory holds %v, want only the target file", names)
	}
}

// TestWriteFileAtomicPreservesOnFailure checks a write that cannot complete
// leaves the previous file intact rather than truncated.
func TestWriteFileAtomicPreservesOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acct.json")

	if err := writeFileAtomic(path, []byte(`{"good":true}`), 0o600); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	// A path whose parent does not exist cannot be written.
	bad := filepath.Join(dir, "missing-dir", "acct.json")
	if err := writeFileAtomic(bad, []byte("x"), 0o600); err == nil {
		t.Fatal("expected the write to fail")
	}

	got, _ := os.ReadFile(path)
	if string(got) != `{"good":true}` {
		t.Fatalf("the original file was disturbed: %s", got)
	}
}

// --- single flight ---------------------------------------------------------

// TestRefreshGroupCollapsesConcurrentCalls is the core guarantee: N concurrent
// refreshes of one account reach the provider once.
func TestRefreshGroupCollapsesConcurrentCalls(t *testing.T) {
	g := newRefreshGroup()

	var calls int64
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release everyone at once
			_ = g.Do("acct-1", func() error {
				atomic.AddInt64(&calls, 1)
				time.Sleep(20 * time.Millisecond)
				return nil
			})
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("provider calls = %d, want 1 for 20 concurrent refreshes", got)
	}
}

// TestRefreshGroupSeparatesKeys checks two accounts are not serialised into one
// refresh.
func TestRefreshGroupSeparatesKeys(t *testing.T) {
	g := newRefreshGroup()
	var calls int64

	var wg sync.WaitGroup
	for _, key := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			_ = g.Do(k, func() error {
				atomic.AddInt64(&calls, 1)
				time.Sleep(10 * time.Millisecond)
				return nil
			})
		}(key)
	}
	wg.Wait()

	if got := atomic.LoadInt64(&calls); got != 3 {
		t.Fatalf("provider calls = %d, want 3 for three distinct accounts", got)
	}
}

// TestRefreshGroupSharesError checks every joiner sees the failure, so callers
// do not proceed as though the refresh succeeded.
func TestRefreshGroupSharesError(t *testing.T) {
	g := newRefreshGroup()
	wantErr := errors.New("provider refused")

	var wg sync.WaitGroup
	errs := make([]error, 10)
	start := make(chan struct{})
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			errs[idx] = g.Do("acct", func() error {
				time.Sleep(10 * time.Millisecond)
				return wantErr
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if !errors.Is(err, wantErr) {
			t.Errorf("caller %d got %v, want the shared error", i, err)
		}
	}
}

// TestRefreshGroupAllowsRetryAfterCompletion checks the key is released, so a
// later refresh is not permanently blocked.
func TestRefreshGroupAllowsRetryAfterCompletion(t *testing.T) {
	g := newRefreshGroup()
	var calls int64
	fn := func() error { atomic.AddInt64(&calls, 1); return nil }

	for i := 0; i < 3; i++ {
		if err := g.Do("acct", fn); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&calls); got != 3 {
		t.Fatalf("sequential calls = %d, want 3 (dedup must only apply to concurrent calls)", got)
	}
}

// TestRefresherSingleFlightsConcurrentRefreshes exercises the wiring rather
// than the group in isolation.
func TestRefresherSingleFlightsConcurrentRefreshes(t *testing.T) {
	p := NewAccountPool()
	acc := &Account{
		ID:          "q1",
		Type:        TypeQoder,
		AccessToken: "pt-x",
		ExpiresAt:   time.Now().Add(time.Minute),
	}
	p.AddOrUpdate(acc)

	var calls int64
	r := NewRefresher(p, time.Minute)
	r.RegisterHandler(TypeQoder, func(ctx context.Context, a *Account) error {
		atomic.AddInt64(&calls, 1)
		time.Sleep(20 * time.Millisecond)
		return nil
	})

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = r.RefreshAccount(context.Background(), acc)
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("handler calls = %d, want 1 for 15 concurrent refreshes of one account", got)
	}
}

// --- directory watcher -----------------------------------------------------

func writeCredFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

const credBody = `{"type":"workbuddy","access_token":"tok","refresh_token":"ref","uid":"u1","realm":"cn"}`

// TestWatcherPicksUpNewCredential is the hot-plug behaviour: dropping a file in
// adds the account without a restart.
func TestWatcherPicksUpNewCredential(t *testing.T) {
	dir := t.TempDir()
	p := NewAccountPool()
	w := NewWatcher(p, []string{dir})
	w.snapshot()

	if got := p.Count(TypeWorkBuddy); got != 0 {
		t.Fatalf("pool starts with %d accounts, want 0", got)
	}

	writeCredFile(t, dir, "new.json", credBody)
	if changed := w.Scan(); changed != 1 {
		t.Fatalf("Scan applied %d changes, want 1", changed)
	}
	if got := p.Count(TypeWorkBuddy); got != 1 {
		t.Fatalf("pool holds %d accounts, want 1", got)
	}
}

// TestWatcherRemovesWithdrawnCredential checks deleting a file takes the account
// out of rotation rather than leaving it serving traffic from memory.
func TestWatcherRemovesWithdrawnCredential(t *testing.T) {
	dir := t.TempDir()
	p := NewAccountPool()
	path := writeCredFile(t, dir, "acct.json", credBody)

	w := NewWatcher(p, []string{dir})
	w.Scan()
	if p.Count(TypeWorkBuddy) != 1 {
		t.Fatal("account was not loaded")
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	w.Scan()

	if got := p.Count(TypeWorkBuddy); got != 0 {
		t.Fatalf("pool still holds %d accounts after the credential was removed", got)
	}
}

// TestWatcherIgnoresUnchangedFiles checks a rescan does not re-apply and log
// every file on every tick.
func TestWatcherIgnoresUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	p := NewAccountPool()
	w := NewWatcher(p, []string{dir})

	writeCredFile(t, dir, "acct.json", credBody)
	w.Scan()

	for i := 0; i < 3; i++ {
		if changed := w.Scan(); changed != 0 {
			t.Fatalf("tick %d re-applied %d unchanged files", i, changed)
		}
	}
}

// TestWatcherAppliesModifiedCredential checks a rotated token in an existing
// file is picked up.
func TestWatcherAppliesModifiedCredential(t *testing.T) {
	dir := t.TempDir()
	p := NewAccountPool()
	w := NewWatcher(p, []string{dir})

	writeCredFile(t, dir, "acct.json", credBody)
	w.Scan()

	updated := `{"type":"workbuddy","access_token":"tok-2","refresh_token":"ref","uid":"u1","realm":"cn"}`
	writeCredFile(t, dir, "acct.json", updated)

	if changed := w.Scan(); changed != 1 {
		t.Fatalf("Scan applied %d changes, want 1 for a modified file", changed)
	}

	acc := p.Get("acct")
	if acc == nil {
		t.Fatal("account vanished")
	}
	if acc.AccessToken != "tok-2" {
		t.Fatalf("access token = %q, want the updated value", acc.AccessToken)
	}
}

// TestWatcherIgnoresTempFiles checks an in-progress atomic write is not mistaken
// for a credential.
func TestWatcherIgnoresTempFiles(t *testing.T) {
	dir := t.TempDir()
	p := NewAccountPool()
	w := NewWatcher(p, []string{dir})

	writeCredFile(t, dir, "acct.json.tmp-12345", credBody)

	if changed := w.Scan(); changed != 0 {
		t.Fatalf("a temp file was loaded as a credential (%d changes)", changed)
	}
}

// TestWatcherIgnoresUnsupportedExtensions checks unrelated files are skipped.
func TestWatcherIgnoresUnsupportedExtensions(t *testing.T) {
	dir := t.TempDir()
	p := NewAccountPool()
	w := NewWatcher(p, []string{dir})

	writeCredFile(t, dir, "notes.txt", credBody)
	writeCredFile(t, dir, "sample.json", credBody)

	if changed := w.Scan(); changed != 0 {
		t.Fatalf("unrelated files were loaded (%d changes)", changed)
	}
}

// TestWatcherHandlesEncryptedCredential checks the encrypted case does not abort
// the scan of the remaining files.
func TestWatcherHandlesEncryptedCredential(t *testing.T) {
	dir := t.TempDir()
	p := NewAccountPool()
	w := NewWatcher(p, []string{dir})

	env := `{"$wbEncrypted":1,"envelope":"eyJzdWl0ZSI6MX0="}`
	writeCredFile(t, dir, "sealed.info",
		`{"account":{"uid":"u1","phoneNumber":`+env+`},"auth":{"accessToken":`+env+`,,"domain":"x"}}`)
	writeCredFile(t, dir, "good.json", credBody)

	// The sealed file must not stop the good one from loading.
	if changed := w.Scan(); changed != 1 {
		t.Fatalf("Scan applied %d changes, want the good file loaded", changed)
	}
	if p.Count(TypeWorkBuddy) != 1 {
		t.Fatalf("pool holds %d accounts, want 1", p.Count(TypeWorkBuddy))
	}
}

// TestWatcherMissingDirectoryIsNotFatal checks a configured-but-absent
// directory does not break the scan.
func TestWatcherMissingDirectoryIsNotFatal(t *testing.T) {
	p := NewAccountPool()
	w := NewWatcher(p, []string{filepath.Join(t.TempDir(), "does-not-exist")})
	if changed := w.Scan(); changed != 0 {
		t.Fatalf("scan of a missing directory applied %d changes", changed)
	}
}

func TestWatcherStopIsIdempotent(t *testing.T) {
	p := NewAccountPool()
	w := NewWatcher(p, []string{t.TempDir()})
	w.Start()
	w.Stop()
	w.Stop() // must not panic on a second close
}

// TestWatcherSkipsBlankDirectories checks an empty config entry (a missing env
// var, for instance) does not resolve to the current directory.
func TestWatcherSkipsBlankDirectories(t *testing.T) {
	p := NewAccountPool()
	w := NewWatcher(p, []string{"", "   "})
	if files := w.credentialFilesLocked(); len(files) != 0 {
		t.Fatalf("a blank directory produced %v", files)
	}
}
