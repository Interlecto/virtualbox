package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/interlecto/virtualbox/internal/backend"
	"github.com/interlecto/virtualbox/internal/policy"
	"github.com/interlecto/virtualbox/internal/queue"
)

type sent struct{ from, to, body string }

// fakeSender answers per target: results[target] (nil = success).
type fakeSender struct {
	mu      sync.Mutex
	results map[string]error
	sent    []sent
}

func (f *fakeSender) Send(_ context.Context, from, to string, open func() (io.ReadCloser, error)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.results[to]; err != nil {
		return err
	}
	r, err := open()
	if err != nil {
		return err
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	f.sent = append(f.sent, sent{from: from, to: to, body: string(b)})
	return nil
}

// localResolver: "local.test" is ours with fixed rules; anything else is foreign.
type localResolver map[string][]policy.Action

func (l localResolver) Resolve(_ context.Context, rcpt string) ([]policy.Action, error) {
	if !strings.HasSuffix(rcpt, "@local.test") {
		return nil, policy.ErrUnknownDomain
	}
	if a, ok := l[rcpt]; ok {
		return a, nil
	}
	return nil, policy.ErrUnknownRecipient
}

func fwd(targets ...string) []policy.Action {
	return []policy.Action{{Kind: policy.KindForward, Targets: targets}}
}

type env struct {
	t     *testing.T
	q     *queue.FileQueue
	s     *fakeSender
	d     *Dispatcher
	clock time.Time
}

func newEnv(t *testing.T, res localResolver, mutate func(*Config)) *env {
	t.Helper()
	e := &env{t: t, clock: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC), s: &fakeSender{results: map[string]error{}}}
	q, err := queue.OpenFileQueue(t.TempDir(), queue.FileOptions{Now: func() time.Time { return e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	e.q = q
	cfg := Config{
		Hostname:      "mx.local.test",
		Queue:         q,
		Sender:        e.s,
		Resolver:      res,
		RewriteSender: KeepSender,
		Now:           func() time.Time { return e.clock },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	return e
}

func (e *env) enqueue(sender, rcpt string, actions []policy.Action) {
	e.t.Helper()
	raw, _ := json.Marshal(actions)
	if _, err := e.q.Enqueue(context.Background(),
		[]queue.Envelope{{Sender: sender, Recipient: rcpt, Action: raw}},
		strings.NewReader("Subject: test\r\n\r\nbody\r\n")); err != nil {
		e.t.Fatal(err)
	}
}

// drain processes everything ready now.
func (e *env) drain() {
	for i := 0; i < 100 && e.d.RunOnce(context.Background()); i++ {
	}
}

func (e *env) targets() []string {
	var out []string
	for _, s := range e.s.sent {
		out = append(out, s.to)
	}
	sort.Strings(out)
	return out
}

func (e *env) depth() int {
	w, f := e.q.Depth()
	return w + f
}

func TestDispatcherSplitsAndExpandsLocalTargets(t *testing.T) {
	res := localResolver{
		"sales@local.test": fwd("ana@ext.test", "team@local.test"),
		"team@local.test":  fwd("bob@ext.test", "ana@ext.test", "sales@local.test"), // dup + cycle
	}
	e := newEnv(t, res, nil)
	e.enqueue("someone@src.test", "info@local.test", fwd("carl@ext.test", "sales@local.test"))
	e.drain()

	want := []string{"ana@ext.test", "bob@ext.test", "carl@ext.test"}
	if got := e.targets(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("delivered to %v, want %v", got, want)
	}
	for _, s := range e.s.sent {
		if s.from != "someone@src.test" || !strings.Contains(s.body, "Subject: test") {
			t.Fatalf("bad delivery %+v", s)
		}
	}
	if n := e.depth(); n != 0 {
		t.Fatalf("queue not empty: %d", n)
	}
}

func TestDispatcherRetriesOnlyFailedTarget(t *testing.T) {
	e := newEnv(t, localResolver{}, nil)
	e.s.results["slow@ext.test"] = &Error{Status: "4.7.1", Msg: "greylisted"}
	e.enqueue("a@src.test", "info@local.test", fwd("ok@ext.test", "slow@ext.test"))
	e.drain()
	if got := e.targets(); len(got) != 1 || got[0] != "ok@ext.test" {
		t.Fatalf("first pass: %v", got)
	}
	if e.depth() != 1 {
		t.Fatalf("failed target should stay queued, depth %d", e.depth())
	}

	// Not ready before the backoff.
	e.drain()
	if len(e.s.sent) != 1 {
		t.Fatal("retried too early")
	}
	delete(e.s.results, "slow@ext.test")
	e.clock = e.clock.Add(6 * time.Minute)
	e.drain()
	if got := e.targets(); len(got) != 2 {
		t.Fatalf("after retry: %v", got)
	}
	if e.depth() != 0 {
		t.Fatalf("queue not empty: %d", e.depth())
	}
}

func TestDispatcherBouncesPermanentFailure(t *testing.T) {
	e := newEnv(t, localResolver{}, nil)
	e.s.results["gone@ext.test"] = &Error{Permanent: true, Status: "5.1.1", Msg: "550 no such user", Host: "mx.ext.test"}
	e.enqueue("alice@src.test", "info@local.test", fwd("gone@ext.test"))
	e.drain()

	if len(e.s.sent) != 1 {
		t.Fatalf("expected only the DSN, got %+v", e.s.sent)
	}
	b := e.s.sent[0]
	if b.from != "" || b.to != "alice@src.test" {
		t.Fatalf("DSN envelope %q -> %q", b.from, b.to)
	}
	for _, want := range []string{"Final-Recipient: rfc822; info@local.test", "Status: 5.1.1", "no such user", "Subject: test"} {
		if !strings.Contains(b.body, want) {
			t.Errorf("DSN lacks %q", want)
		}
	}
	if e.depth() != 0 {
		t.Fatalf("queue not empty: %d", e.depth())
	}
}

func TestDispatcherNoBounceToNullSender(t *testing.T) {
	e := newEnv(t, localResolver{}, nil)
	e.s.results["gone@ext.test"] = &Error{Permanent: true, Status: "5.1.1", Msg: "no such user"}
	e.enqueue("", "info@local.test", fwd("gone@ext.test"))
	e.drain()
	if len(e.s.sent) != 0 || e.depth() != 0 {
		t.Fatalf("sent %+v depth %d", e.s.sent, e.depth())
	}
}

func TestDispatcherExpiry(t *testing.T) {
	e := newEnv(t, localResolver{}, func(c *Config) {
		c.Retry = queue.RetryPolicy{Initial: time.Minute, Max: time.Minute, MaxAge: 3 * time.Minute}
	})
	e.s.results["slow@ext.test"] = &Error{Status: "4.4.1", Msg: "connection refused"}
	e.enqueue("alice@src.test", "info@local.test", fwd("slow@ext.test"))
	for i := 0; i < 5; i++ {
		e.drain()
		e.clock = e.clock.Add(61 * time.Second)
	}
	if len(e.s.sent) != 1 || e.s.sent[0].to != "alice@src.test" || !strings.Contains(e.s.sent[0].body, "Status: 4.4.7") {
		t.Fatalf("expected expiry DSN, got %+v", e.s.sent)
	}
	if e.depth() != 0 {
		t.Fatalf("queue not empty: %d", e.depth())
	}
}

func TestDispatcherLocalBounceAndUnknown(t *testing.T) {
	res := localResolver{"alice@local.test": fwd("alice@ext.test"), "loop@local.test": fwd("loop2@local.test"), "loop2@local.test": fwd("loop@local.test")}
	e := newEnv(t, res, nil)
	// Unknown local target: permanent, DSN goes to a sender in our own
	// domain, which is resolved locally rather than sent to ourselves.
	e.enqueue("alice@local.test", "x@local.test", fwd("ghost@local.test"))
	e.drain()
	if len(e.s.sent) != 1 || e.s.sent[0].to != "alice@ext.test" || !strings.Contains(e.s.sent[0].body, "Status: 5.1.1") {
		t.Fatalf("got %+v", e.s.sent)
	}

	e.s.sent = nil
	e.enqueue("", "x@local.test", fwd("loop@local.test"))
	e.drain()
	if len(e.s.sent) != 0 || e.depth() != 0 {
		t.Fatalf("loop: sent %+v depth %d", e.s.sent, e.depth())
	}
}

type memBackend struct {
	got []backend.Message
	err error
}

func (m *memBackend) Name() string { return "mem" }
func (m *memBackend) Deliver(_ context.Context, msg backend.Message, _ io.Reader) error {
	if m.err != nil {
		return m.err
	}
	m.got = append(m.got, msg)
	return nil
}

func TestDispatcherStoreAndUnimplemented(t *testing.T) {
	mb := &memBackend{}
	reg := backend.NewRegistry()
	if err := reg.Register(mb); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, localResolver{}, func(c *Config) { c.Backends = reg })
	e.enqueue("a@src.test", "box@local.test", []policy.Action{{Kind: policy.KindStore, Backend: "mem"}})
	e.drain()
	if len(mb.got) != 1 || mb.got[0].Recipient != "box@local.test" || e.depth() != 0 {
		t.Fatalf("store: %+v depth %d", mb.got, e.depth())
	}

	e.enqueue("a@src.test", "box@local.test", []policy.Action{{Kind: policy.KindAutoReply}})
	e.drain()
	if e.depth() != 1 {
		t.Fatalf("unimplemented action should be retried, depth %d", e.depth())
	}

	mb.err = &backend.PermanentError{Err: errors.New("mailbox full")}
	e.enqueue("", "box@local.test", []policy.Action{{Kind: policy.KindStore, Backend: "mem"}})
	e.drain()
	if e.depth() != 1 {
		t.Fatalf("permanent backend failure should drop the entry, depth %d", e.depth())
	}
}

func TestDispatcherRun(t *testing.T) {
	e := newEnv(t, localResolver{}, func(c *Config) { c.PollInterval = 10 * time.Millisecond; c.Now = time.Now })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.d.Run(ctx); close(done) }()
	e.enqueue("a@src.test", "info@local.test", fwd("x@ext.test", "y@ext.test"))
	deadline := time.Now().Add(5 * time.Second)
	for e.depth() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	if len(e.s.sent) != 2 {
		t.Fatalf("sent %+v", e.s.sent)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty config accepted")
	}
}
