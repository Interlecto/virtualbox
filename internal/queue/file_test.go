package queue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func open(t *testing.T, dir string) *FileQueue {
	t.Helper()
	q, err := OpenFileQueue(dir, FileOptions{Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func env(rcpt string) Envelope {
	return Envelope{Sender: "a@example.com", Recipient: rcpt, Action: []byte(`{"kind":"forward"}`)}
}

func readBody(t *testing.T, q Queue, id string) string {
	t.Helper()
	r, err := q.Body(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(files)
}

func TestEnqueueClaimAck(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	q := open(t, dir)

	ids, err := q.Enqueue(ctx, []Envelope{env("x@example.org")}, bytes.NewReader([]byte("Subject: hi\r\n\r\nbody")))
	if err != nil || len(ids) != 1 {
		t.Fatalf("enqueue: %v %v", ids, err)
	}

	e, err := q.Claim(ctx, t0)
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != ids[0] || e.Envelope.Recipient != "x@example.org" || string(e.Envelope.Action) != `{"kind":"forward"}` {
		t.Fatalf("unexpected entry %+v", e)
	}
	if got := readBody(t, q, e.ID); got != "Subject: hi\r\n\r\nbody" {
		t.Fatalf("body = %q", got)
	}
	if _, err := q.Claim(ctx, t0); !errors.Is(err, ErrEmpty) {
		t.Fatalf("second claim: want ErrEmpty, got %v", err)
	}

	if err := q.Ack(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := q.Ack(ctx, e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double ack: want ErrNotFound, got %v", err)
	}
	if _, err := q.Body(ctx, e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("body after ack: want ErrNotFound, got %v", err)
	}
	if n := countFiles(t, filepath.Join(dir, dirBodies)) + countFiles(t, filepath.Join(dir, dirEntries)); n != 0 {
		t.Fatalf("%d files left after ack", n)
	}
}

func TestEnqueueRequiresEnvelope(t *testing.T) {
	q := open(t, t.TempDir())
	if _, err := q.Enqueue(context.Background(), nil, bytes.NewReader(nil)); err == nil {
		t.Fatal("expected error for empty envelope list")
	}
}

func TestSharedBodyRefCount(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	q := open(t, dir)

	ids, err := q.Enqueue(ctx, []Envelope{env("a@x.org"), env("b@x.org")}, bytes.NewReader([]byte("shared")))
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	if n := countFiles(t, filepath.Join(dir, dirBodies)); n != 1 {
		t.Fatalf("want 1 shared body, got %d", n)
	}
	if err := q.Ack(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if got := readBody(t, q, ids[1]); got != "shared" {
		t.Fatalf("body lost after first ack: %q", got)
	}
	if err := q.Fail(ctx, ids[1], errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, filepath.Join(dir, dirBodies)); n != 0 {
		t.Fatalf("body not removed after last entry: %d", n)
	}
}

func TestRetrySchedulesAndCountsAttempts(t *testing.T) {
	ctx := context.Background()
	q := open(t, t.TempDir())
	ids, _ := q.Enqueue(ctx, []Envelope{env("a@x.org")}, bytes.NewReader([]byte("m")))

	e, _ := q.Claim(ctx, t0)
	next := t0.Add(10 * time.Minute)
	if err := q.Retry(ctx, e.ID, next, errors.New("greylisted")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx, t0.Add(9*time.Minute)); !errors.Is(err, ErrEmpty) {
		t.Fatalf("claimed before NextAttempt: %v", err)
	}
	e2, err := q.Claim(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	if e2.ID != ids[0] || e2.Attempts != 1 || e2.LastError != "greylisted" || !e2.NextAttempt.Equal(next) {
		t.Fatalf("unexpected retried entry %+v", e2)
	}
}

func TestRetryNotInFlight(t *testing.T) {
	ctx := context.Background()
	q := open(t, t.TempDir())
	ids, _ := q.Enqueue(ctx, []Envelope{env("a@x.org")}, bytes.NewReader([]byte("m")))
	if err := q.Retry(ctx, ids[0], t0, nil); err == nil {
		t.Fatal("retry of an entry that was not claimed should fail")
	}
	if err := q.Retry(ctx, "nope", t0, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestClaimOrder(t *testing.T) {
	ctx := context.Background()
	q := open(t, t.TempDir())
	var ids []string
	for i := 0; i < 3; i++ {
		got, err := q.Enqueue(ctx, []Envelope{env(fmt.Sprintf("u%d@x.org", i))}, bytes.NewReader([]byte("m")))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, got...)
	}
	// Push the first entry into the future; the others must come out first.
	e, _ := q.Claim(ctx, t0)
	if err := q.Retry(ctx, e.ID, t0.Add(time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		got, err := q.Claim(ctx, t0)
		if err != nil {
			t.Fatal(err)
		}
		seen[got.ID] = true
	}
	if seen[e.ID] || len(seen) != 2 {
		t.Fatalf("unexpected order: %v (delayed %s)", seen, e.ID)
	}
	if _, err := q.Claim(ctx, t0); !errors.Is(err, ErrEmpty) {
		t.Fatalf("want ErrEmpty, got %v", err)
	}
	if w, f := q.Depth(); w != 1 || f != 2 {
		t.Fatalf("depth = %d waiting, %d in flight", w, f)
	}
	_ = ids
}

func TestPersistenceAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	q := open(t, dir)
	ids, err := q.Enqueue(ctx, []Envelope{env("a@x.org"), env("b@x.org")}, bytes.NewReader([]byte("persist me")))
	if err != nil {
		t.Fatal(err)
	}
	// One entry is in flight, one retried: both must survive a restart.
	e, _ := q.Claim(ctx, t0)
	if err := q.Retry(ctx, e.ID, t0.Add(time.Minute), errors.New("later")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx, t0); err != nil {
		t.Fatal(err)
	}

	q2 := open(t, dir)
	if w, f := q2.Depth(); w != 2 || f != 0 {
		t.Fatalf("after restart: %d waiting, %d in flight", w, f)
	}
	got := map[string]*Entry{}
	for i := 0; i < 2; i++ {
		c, err := q2.Claim(ctx, t0.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		got[c.ID] = c
	}
	for _, id := range ids {
		if got[id] == nil {
			t.Fatalf("entry %s lost on restart", id)
		}
	}
	if got[e.ID].Attempts != 1 || got[e.ID].LastError != "later" {
		t.Fatalf("retry state lost: %+v", got[e.ID])
	}
	if readBody(t, q2, e.ID) != "persist me" {
		t.Fatal("body lost on restart")
	}
}

func TestClaimTimeoutReclaims(t *testing.T) {
	ctx := context.Background()
	q, err := OpenFileQueue(t.TempDir(), FileOptions{ClaimTimeout: time.Minute, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := q.Enqueue(ctx, []Envelope{env("a@x.org")}, bytes.NewReader([]byte("m")))
	if _, err := q.Claim(ctx, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx, t0.Add(30*time.Second)); !errors.Is(err, ErrEmpty) {
		t.Fatalf("entry given back too early: %v", err)
	}
	e, err := q.Claim(ctx, t0.Add(time.Minute))
	if err != nil || e.ID != ids[0] {
		t.Fatalf("stale claim not reclaimed: %v %v", e, err)
	}
}

func TestOpenCleansTmpAndOrphanBodies(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	q := open(t, dir)
	if _, err := q.Enqueue(ctx, []Envelope{env("a@x.org")}, bytes.NewReader([]byte("keep"))); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, dirBodies, "deadbeef"+bodyExt)
	stray := filepath.Join(dir, dirTmp, "w-partial")
	for _, p := range []string{orphan, stray} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	q2 := open(t, dir)
	for _, p := range []string{orphan, stray} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s should have been removed (err=%v)", p, err)
		}
	}
	if w, _ := q2.Depth(); w != 1 {
		t.Fatalf("real entry lost: %d waiting", w)
	}
}

func TestOpenRejectsCorruptEntry(t *testing.T) {
	dir := t.TempDir()
	open(t, dir)
	if err := os.WriteFile(filepath.Join(dir, dirEntries, "bad"+entryExt), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileQueue(dir, FileOptions{}); err == nil {
		t.Fatal("corrupt entry should make Open fail")
	}
}

func TestConcurrentUse(t *testing.T) {
	ctx := context.Background()
	q := open(t, t.TempDir())
	const producers, perProducer = 8, 25

	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				rcpt := fmt.Sprintf("p%d-%d@x.org", p, i)
				if _, err := q.Enqueue(ctx, []Envelope{env(rcpt)}, bytes.NewReader([]byte("m"))); err != nil {
					t.Error(err)
					return
				}
			}
		}(p)
	}
	wg.Wait()

	var mu sync.Mutex
	seen := map[string]bool{}
	for c := 0; c < 4; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				e, err := q.Claim(ctx, t0)
				if errors.Is(err, ErrEmpty) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				dup := seen[e.ID]
				seen[e.ID] = true
				mu.Unlock()
				if dup {
					t.Errorf("entry %s claimed twice", e.ID)
				}
				if err := q.Ack(ctx, e.ID); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if len(seen) != producers*perProducer {
		t.Fatalf("processed %d entries, want %d", len(seen), producers*perProducer)
	}
}
