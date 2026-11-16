package queue

import (
	"container/heap"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// On-disk layout under the queue directory:
//
//	tmp/              files being written (cleaned at startup)
//	bodies/<id>.eml   message bodies, shared by the entries of one Enqueue
//	entries/<id>.json one file per entry (metadata)
//
// Every file becomes visible only through an atomic rename after fsync, so a
// crash never leaves a half-written file in bodies/ or entries/. A crash
// during Enqueue may leave an orphan body (removed at startup) or, for
// multi-recipient messages, only some entries; since the client never got
// "250 OK" it will retry, which is consistent with at-least-once delivery.
const (
	dirTmp     = "tmp"
	dirBodies  = "bodies"
	dirEntries = "entries"
	bodyExt    = ".eml"
	entryExt   = ".json"

	defaultClaimTimeout = 15 * time.Minute
)

// FileOptions configures a FileQueue. The zero value is valid.
type FileOptions struct {
	// ClaimTimeout is how long an in-flight entry may stay unacknowledged
	// before it becomes ready again. Default 15 minutes.
	ClaimTimeout time.Duration
	// Now overrides the clock used to stamp new entries (tests only).
	Now func() time.Time
}

// FileQueue is a Queue backed by files in a directory. It uses only the
// standard library and keeps an in-memory index rebuilt at startup.
type FileQueue struct {
	dir  string
	opts FileOptions

	mu       sync.Mutex
	entries  map[string]*item     // every entry, by ID
	waiting  waitHeap             // entries not in flight, ordered by NextAttempt
	claimed  map[string]time.Time // in-flight entries and when they were claimed
	bodyRefs map[string]int       // body ID -> number of entries referencing it
}

var _ Queue = (*FileQueue)(nil)

type item struct {
	e     Entry
	index int // position in waiting, -1 when not in the heap
}

// OpenFileQueue opens (creating it if needed) the queue stored in dir.
// Entries that were in flight when the process stopped become ready again.
func OpenFileQueue(dir string, opts FileOptions) (*FileQueue, error) {
	if opts.ClaimTimeout <= 0 {
		opts.ClaimTimeout = defaultClaimTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	q := &FileQueue{
		dir:      dir,
		opts:     opts,
		entries:  make(map[string]*item),
		claimed:  make(map[string]time.Time),
		bodyRefs: make(map[string]int),
	}
	for _, sub := range []string{dirTmp, dirBodies, dirEntries} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, fmt.Errorf("queue: %w", err)
		}
	}
	if err := q.load(); err != nil {
		return nil, err
	}
	return q, nil
}

func (q *FileQueue) load() error {
	// Leftovers from interrupted writes.
	tmps, err := os.ReadDir(filepath.Join(q.dir, dirTmp))
	if err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	for _, t := range tmps {
		_ = os.Remove(filepath.Join(q.dir, dirTmp, t.Name()))
	}

	files, err := os.ReadDir(filepath.Join(q.dir, dirEntries))
	if err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasSuffix(name, entryExt) {
			continue
		}
		path := filepath.Join(q.dir, dirEntries, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("queue: read %s: %w", path, err)
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			return fmt.Errorf("queue: corrupt entry %s: %w", path, err)
		}
		if e.ID+entryExt != name || e.BodyID == "" {
			return fmt.Errorf("queue: inconsistent entry %s", path)
		}
		if _, err := os.Stat(q.bodyPath(e.BodyID)); err != nil {
			return fmt.Errorf("queue: entry %s has no body: %w", e.ID, err)
		}
		it := &item{e: e, index: -1}
		q.entries[e.ID] = it
		q.bodyRefs[e.BodyID]++
		heap.Push(&q.waiting, it)
	}

	// Bodies no entry refers to (crash during Enqueue).
	bodies, err := os.ReadDir(filepath.Join(q.dir, dirBodies))
	if err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	for _, b := range bodies {
		id := strings.TrimSuffix(b.Name(), bodyExt)
		if q.bodyRefs[id] == 0 {
			_ = os.Remove(filepath.Join(q.dir, dirBodies, b.Name()))
		}
	}
	return nil
}

func (q *FileQueue) bodyPath(bodyID string) string {
	return filepath.Join(q.dir, dirBodies, bodyID+bodyExt)
}

func (q *FileQueue) entryPath(id string) string {
	return filepath.Join(q.dir, dirEntries, id+entryExt)
}

// Enqueue implements Queue.
func (q *FileQueue) Enqueue(ctx context.Context, envs []Envelope, body io.Reader) ([]string, error) {
	if len(envs) == 0 {
		return nil, errors.New("queue: at least one envelope is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	bodyID, err := newID()
	if err != nil {
		return nil, err
	}
	err = q.writeAtomic(q.bodyPath(bodyID), func(w io.Writer) error {
		_, err := io.Copy(w, body)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("queue: store body: %w", err)
	}
	if err := syncDir(filepath.Join(q.dir, dirBodies)); err != nil {
		_ = os.Remove(q.bodyPath(bodyID))
		return nil, fmt.Errorf("queue: sync bodies: %w", err)
	}

	now := q.opts.Now()
	created := make([]*item, 0, len(envs))
	rollback := func() {
		for _, it := range created {
			_ = os.Remove(q.entryPath(it.e.ID))
		}
		_ = os.Remove(q.bodyPath(bodyID))
	}
	for _, env := range envs {
		id, err := newID()
		if err != nil {
			rollback()
			return nil, err
		}
		e := Entry{
			ID:          id,
			BodyID:      bodyID,
			Envelope:    env,
			ReceivedAt:  now,
			NextAttempt: now,
		}
		if err := q.writeEntry(e); err != nil {
			rollback()
			return nil, fmt.Errorf("queue: store entry: %w", err)
		}
		created = append(created, &item{e: e, index: -1})
	}
	if err := syncDir(filepath.Join(q.dir, dirEntries)); err != nil {
		rollback()
		return nil, fmt.Errorf("queue: sync entries: %w", err)
	}

	ids := make([]string, len(created))
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, it := range created {
		q.entries[it.e.ID] = it
		heap.Push(&q.waiting, it)
		ids[i] = it.e.ID
	}
	q.bodyRefs[bodyID] += len(created)
	return ids, nil
}

// Claim implements Queue.
func (q *FileQueue) Claim(ctx context.Context, now time.Time) (*Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	// Give back entries whose worker never reported.
	for id, at := range q.claimed {
		if now.Sub(at) >= q.opts.ClaimTimeout {
			delete(q.claimed, id)
			heap.Push(&q.waiting, q.entries[id])
		}
	}

	if len(q.waiting) == 0 || q.waiting[0].e.NextAttempt.After(now) {
		return nil, ErrEmpty
	}
	it := heap.Pop(&q.waiting).(*item)
	q.claimed[it.e.ID] = now
	e := it.e
	return &e, nil
}

// Body implements Queue.
func (q *FileQueue) Body(ctx context.Context, id string) (io.ReadCloser, error) {
	q.mu.Lock()
	it, ok := q.entries[id]
	var bodyID string
	if ok {
		bodyID = it.e.BodyID
	}
	q.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	f, err := os.Open(q.bodyPath(bodyID))
	if err != nil {
		return nil, fmt.Errorf("queue: open body: %w", err)
	}
	return f, nil
}

// Ack implements Queue.
func (q *FileQueue) Ack(ctx context.Context, id string) error {
	return q.remove(id)
}

// Fail implements Queue. The cause is not persisted: the caller reports it
// in the DSN.
func (q *FileQueue) Fail(ctx context.Context, id string, cause error) error {
	return q.remove(id)
}

// Retry implements Queue.
func (q *FileQueue) Retry(ctx context.Context, id string, next time.Time, cause error) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	it, ok := q.entries[id]
	if !ok {
		return ErrNotFound
	}
	if _, inFlight := q.claimed[id]; !inFlight {
		return fmt.Errorf("queue: entry %s is not in flight", id)
	}
	e := it.e
	e.Attempts++
	e.NextAttempt = next
	if cause != nil {
		e.LastError = cause.Error()
	}
	if err := q.writeEntry(e); err != nil {
		return fmt.Errorf("queue: update entry: %w", err)
	}
	if err := syncDir(filepath.Join(q.dir, dirEntries)); err != nil {
		return fmt.Errorf("queue: sync entries: %w", err)
	}
	it.e = e
	delete(q.claimed, id)
	heap.Push(&q.waiting, it)
	return nil
}

// Depth reports how many entries are waiting (including scheduled retries)
// and how many are in flight. Intended for statistics.
func (q *FileQueue) Depth() (waiting, inFlight int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.waiting), len(q.claimed)
}

func (q *FileQueue) remove(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	it, ok := q.entries[id]
	if !ok {
		return ErrNotFound
	}
	if err := os.Remove(q.entryPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("queue: remove entry: %w", err)
	}
	if err := syncDir(filepath.Join(q.dir, dirEntries)); err != nil {
		return fmt.Errorf("queue: sync entries: %w", err)
	}
	if it.index >= 0 {
		heap.Remove(&q.waiting, it.index)
	}
	delete(q.claimed, id)
	delete(q.entries, id)

	bodyID := it.e.BodyID
	q.bodyRefs[bodyID]--
	if q.bodyRefs[bodyID] <= 0 {
		delete(q.bodyRefs, bodyID)
		if err := os.Remove(q.bodyPath(bodyID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("queue: remove body: %w", err)
		}
	}
	return nil
}

func (q *FileQueue) writeEntry(e Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return q.writeAtomic(q.entryPath(e.ID), func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// writeAtomic writes through a temp file, fsyncs it and renames it into
// place. The caller is responsible for syncing the destination directory.
func (q *FileQueue) writeAtomic(final string, write func(io.Writer) error) error {
	f, err := os.CreateTemp(filepath.Join(q.dir, dirTmp), "w-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := write(f); err != nil {
		f.Close()
		os.Remove(name)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(name)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, final); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// syncDir flushes directory metadata so renames and removals survive a
// crash. Windows cannot fsync a directory, so it is a no-op there
// (production runs on Linux).
func syncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("queue: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// waitHeap is a min-heap of entries ordered by NextAttempt.
type waitHeap []*item

func (h waitHeap) Len() int { return len(h) }

func (h waitHeap) Less(i, j int) bool {
	a, b := h[i].e, h[j].e
	if !a.NextAttempt.Equal(b.NextAttempt) {
		return a.NextAttempt.Before(b.NextAttempt)
	}
	return a.ReceivedAt.Before(b.ReceivedAt)
}

func (h waitHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *waitHeap) Push(x any) {
	it := x.(*item)
	it.index = len(*h)
	*h = append(*h, it)
}

func (h *waitHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	it.index = -1
	return it
}
