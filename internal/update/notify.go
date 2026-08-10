package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// checkInterval is how often katana asks GitHub for the newest release.
	// Between checks the answer comes from the cache, at no cost.
	checkInterval = 24 * time.Hour

	// checkTimeout bounds the background request. Missing the window costs
	// nothing: the cache stays stale and the next run tries again.
	checkTimeout = 5 * time.Second

	// noticeWait is how long a finished command waits for a check still in
	// flight. Update news is never worth stalling the terminal for.
	noticeWait = time.Second

	// stateFile caches the last check under the user's cache directory.
	stateFile = "update.json"
)

// state is the cached result of the last release check.
type state struct {
	CheckedAt time.Time `json:"checked_at"`
	LatestTag string    `json:"latest_tag"`
}

// Notifier runs katana's once-a-day release check beside the command the user
// asked for, and reports the result once that command is done. The check never
// blocks the work and never fails it: an unreachable network, a private
// repository or a missing token all end in silence.
type Notifier struct {
	current string
	done    chan struct{}

	mu     sync.Mutex
	latest string
	url    string
}

// Start begins a release check unless one ran recently or checking is turned
// off, and returns the notifier to print the outcome with.
func Start(current string) *Notifier {
	n := &Notifier{current: current, done: make(chan struct{})}
	if checkDisabled() || IsDevBuild(current) {
		close(n.done)
		return n
	}

	// A cached tag still counts as news until the user acts on it, so report
	// it every run and only spend a request once the cache has aged out.
	st, _ := loadState()
	n.latest = st.LatestTag
	if time.Since(st.CheckedAt) < checkInterval {
		close(n.done)
		return n
	}

	go func() {
		defer close(n.done)
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		defer cancel()

		rel, err := New(current).Latest(ctx)
		if err != nil {
			return
		}
		n.mu.Lock()
		n.latest, n.url = rel.Tag, rel.URL
		n.mu.Unlock()
		saveState(state{CheckedAt: time.Now(), LatestTag: rel.Tag})
	}()
	return n
}

// Notice writes a one-line upgrade hint to w when a newer release exists. It
// waits briefly for a check still in flight, then gives up.
func (n *Notifier) Notice(w io.Writer) {
	if n == nil {
		return
	}
	timer := time.NewTimer(noticeWait)
	defer timer.Stop()
	select {
	case <-n.done:
	case <-timer.C:
	}

	n.mu.Lock()
	latest, url := n.latest, n.url
	n.mu.Unlock()

	if latest == "" || Compare(n.current, latest) >= 0 {
		return
	}
	fmt.Fprintf(w, "\nkatana %s is available (you have %s). Run `katana update` to install it.\n", latest, n.current)
	if url != "" {
		fmt.Fprintf(w, "%s\n", url)
	}
}

// checkDisabled reports whether the background check should be skipped.
// KATANA_NO_UPDATE_CHECK turns it off outright; CI is skipped because the
// notice would be log noise nobody can act on.
func checkDisabled() bool {
	if os.Getenv("KATANA_NO_UPDATE_CHECK") != "" {
		return true
	}
	return os.Getenv("CI") != ""
}

// stateDir is where katana caches the last check. KATANA_CACHE_DIR overrides
// it.
func stateDir() (string, error) {
	if d := os.Getenv("KATANA_CACHE_DIR"); d != "" {
		return d, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, binaryName), nil
}

func loadState() (state, error) {
	dir, err := stateDir()
	if err != nil {
		return state{}, err
	}
	body, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		return state{}, err
	}
	var st state
	if err := json.Unmarshal(body, &st); err != nil {
		return state{}, err
	}
	return st, nil
}

// saveState records a check. Failures are ignored by callers: a cache that
// cannot be written only means katana asks again next run.
func saveState(st state) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stateFile), body, 0o644)
}
