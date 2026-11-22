package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/interlecto/virtualbox/internal/backend"
	"github.com/interlecto/virtualbox/internal/dsn"
	"github.com/interlecto/virtualbox/internal/policy"
	"github.com/interlecto/virtualbox/internal/queue"
)

// maxLocalDepth bounds the re-resolution of forward targets that belong to
// one of our own domains (alias pointing to alias).
const maxLocalDepth = 8

// maxDSNHeaderBytes is how much of the original message is read to quote its
// headers in a DSN (dsn.Build truncates further).
const maxDSNHeaderBytes = 64 * 1024

// RewriteSenderFunc returns the envelope sender to use when forwarding a
// message from sender (empty = null sender) to target. It is the hook for
// SRS (roadmap 1.5).
type RewriteSenderFunc func(sender, target string) (string, error)

// KeepSender is a RewriteSenderFunc that leaves the sender untouched. It
// breaks SPF for forwarded mail and only exists until SRS (roadmap 1.5).
func KeepSender(sender, _ string) (string, error) { return sender, nil }

// Config configures a Dispatcher.
type Config struct {
	// Hostname is the Reporting-MTA of the DSNs.
	Hostname string
	Queue    queue.Queue
	// Sender performs SMTP deliveries (normally *Client).
	Sender Sender
	// Resolver re-resolves forward targets in our own domains so mail
	// never loops through our own MX.
	Resolver policy.Resolver
	// RewriteSender is required; see RewriteSenderFunc.
	RewriteSender RewriteSenderFunc
	// Backends serves store actions. Nil means none are configured.
	Backends *backend.Registry
	// Retry defaults to queue.DefaultRetryPolicy().
	Retry queue.RetryPolicy
	// Workers is the number of concurrent deliveries (default 4).
	Workers int
	// PollInterval is the wait when the queue is empty (default 1s).
	PollInterval time.Duration
	Logger       *slog.Logger
	// Now defaults to time.Now (tests override it).
	Now func() time.Time
}

// Dispatcher takes entries from the queue and executes their actions.
//
// An entry whose actions expand to several deliveries (several forward
// targets, or targets in our own domains that resolve further) is first
// split: one new entry per delivery is durably enqueued and the original is
// acknowledged, so each delivery is retried and bounced independently and a
// success is never repeated because a sibling failed.
type Dispatcher struct {
	cfg Config
	log *slog.Logger
}

// New validates cfg and returns a Dispatcher.
func New(cfg Config) (*Dispatcher, error) {
	if cfg.Hostname == "" || cfg.Queue == nil || cfg.Sender == nil || cfg.Resolver == nil || cfg.RewriteSender == nil {
		return nil, errors.New("delivery: Hostname, Queue, Sender, Resolver and RewriteSender are required")
	}
	if cfg.Retry == (queue.RetryPolicy{}) {
		cfg.Retry = queue.DefaultRetryPolicy()
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Dispatcher{cfg: cfg, log: log}, nil
}

// Run processes the queue until ctx is cancelled and the in-flight
// deliveries have stopped. Interrupted deliveries are not rescheduled: the
// queue hands their claims back after a restart (at-least-once).
func (d *Dispatcher) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < d.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.worker(ctx)
		}()
	}
	wg.Wait()
}

func (d *Dispatcher) worker(ctx context.Context) {
	for ctx.Err() == nil {
		if !d.RunOnce(ctx) {
			t := time.NewTimer(d.cfg.PollInterval)
			select {
			case <-ctx.Done():
				t.Stop()
			case <-t.C:
			}
		}
	}
}

// RunOnce claims and processes one ready entry. It reports whether there was
// one.
func (d *Dispatcher) RunOnce(ctx context.Context) bool {
	e, err := d.cfg.Queue.Claim(ctx, d.cfg.Now())
	if errors.Is(err, queue.ErrEmpty) {
		return false
	}
	if err != nil {
		if ctx.Err() == nil {
			d.log.Error("queue claim failed", "err", err)
		}
		return false
	}
	d.process(ctx, e)
	return true
}

// delivery is one unit of work: a single action with at most one target.
type delivery = policy.Action

func (d *Dispatcher) process(ctx context.Context, e *queue.Entry) {
	var actions []policy.Action
	if err := json.Unmarshal(e.Envelope.Action, &actions); err != nil {
		d.fail(ctx, e, permanent("5.3.0", "corrupt queue entry: %v", err))
		return
	}

	deliveries, err := d.expand(ctx, actions)
	if err != nil {
		d.outcome(ctx, e, err)
		return
	}
	switch len(deliveries) {
	case 0:
		d.log.Warn("queue entry has no actions, dropping", "id", e.ID)
		d.ack(ctx, e)
	case 1:
		d.outcome(ctx, e, d.deliver(ctx, e, deliveries[0]))
	default:
		d.split(ctx, e, deliveries)
	}
}

// expand flattens actions into single deliveries, replacing forward targets
// in our own domains by what they resolve to.
func (d *Dispatcher) expand(ctx context.Context, actions []policy.Action) ([]delivery, error) {
	var out []delivery
	seen := map[string]bool{}
	var unknown []string
	var walk func(actions []policy.Action, depth int) error
	walk = func(actions []policy.Action, depth int) error {
		for _, a := range actions {
			if a.Kind != policy.KindForward {
				out = append(out, a)
				continue
			}
			for _, t := range a.Targets {
				key := strings.ToLower(t)
				if seen[key] {
					continue
				}
				seen[key] = true
				local, err := d.cfg.Resolver.Resolve(ctx, t)
				switch {
				case errors.Is(err, policy.ErrUnknownDomain):
					out = append(out, policy.Action{Kind: policy.KindForward, Targets: []string{t}, Params: a.Params})
					continue
				case errors.Is(err, policy.ErrUnknownRecipient):
					unknown = append(unknown, t)
					continue
				case err != nil:
					return temporary("4.3.0", "resolve %s: %v", t, err)
				}
				if depth >= maxLocalDepth {
					return temporary("4.4.6", "alias expansion of %s too deep", t)
				}
				if err := walk(local, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(actions, 0); err != nil {
		return nil, err
	}
	if len(out) == 0 && len(actions) > 0 {
		if len(unknown) > 0 {
			return nil, permanent("5.1.1", "unknown local recipient %s", strings.Join(unknown, ", "))
		}
		return nil, permanent("5.4.6", "alias loop: no deliverable target")
	}
	for _, u := range unknown {
		d.log.Warn("forward target in a local domain has no rule, skipped", "target", u)
	}
	return out, nil
}

// split replaces e by one entry per delivery.
func (d *Dispatcher) split(ctx context.Context, e *queue.Entry, deliveries []delivery) {
	envs := make([]queue.Envelope, 0, len(deliveries))
	for _, dl := range deliveries {
		raw, err := json.Marshal([]policy.Action{dl})
		if err != nil {
			d.outcome(ctx, e, temporary("4.3.0", "encode action: %v", err))
			return
		}
		envs = append(envs, queue.Envelope{Sender: e.Envelope.Sender, Recipient: e.Envelope.Recipient, Action: raw})
	}
	body, err := d.cfg.Queue.Body(ctx, e.ID)
	if err != nil {
		d.outcome(ctx, e, temporary("4.3.0", "open body: %v", err))
		return
	}
	defer body.Close()
	ids, err := d.cfg.Queue.Enqueue(ctx, envs, body)
	if err != nil {
		d.outcome(ctx, e, temporary("4.3.0", "split entry: %v", err))
		return
	}
	d.log.Info("queue entry split", "id", e.ID, "into", ids)
	d.ack(ctx, e)
}

// deliver executes one delivery.
func (d *Dispatcher) deliver(ctx context.Context, e *queue.Entry, dl delivery) error {
	open := func() (io.ReadCloser, error) { return d.cfg.Queue.Body(ctx, e.ID) }
	switch dl.Kind {
	case policy.KindForward:
		target := dl.Targets[0]
		from, err := d.cfg.RewriteSender(e.Envelope.Sender, target)
		if err != nil {
			return temporary("4.3.0", "rewrite sender: %v", err)
		}
		return d.cfg.Sender.Send(ctx, from, target, open)
	case policy.KindStore:
		if d.cfg.Backends == nil {
			return temporary("4.3.5", "storage backend %q not configured", dl.Backend)
		}
		b, ok := d.cfg.Backends.Get(dl.Backend)
		if !ok {
			return temporary("4.3.5", "storage backend %q not configured", dl.Backend)
		}
		body, err := open()
		if err != nil {
			return temporary("4.3.0", "open body: %v", err)
		}
		defer body.Close()
		err = b.Deliver(ctx, backend.Message{ID: e.ID, Sender: e.Envelope.Sender, Recipient: e.Envelope.Recipient}, body)
		if err != nil && backend.IsPermanent(err) {
			return permanent("5.3.0", "backend %s: %v", dl.Backend, err)
		}
		return err
	default:
		return temporary("4.3.3", "action %q is not implemented yet", dl.Kind)
	}
}

// outcome acknowledges, reschedules or fails e according to err.
func (d *Dispatcher) outcome(ctx context.Context, e *queue.Entry, err error) {
	if err == nil {
		d.log.Info("delivered", "id", e.ID)
		d.ack(ctx, e)
		return
	}
	if ctx.Err() != nil {
		// Shutting down: leave the claim; it is handed back after restart.
		return
	}
	var de *Error
	if !errors.As(err, &de) {
		de = &Error{Status: "4.0.0", Msg: err.Error()}
	}
	if de.Permanent {
		d.fail(ctx, e, de)
		return
	}
	now := d.cfg.Now()
	next, ok := d.cfg.Retry.Next(*e, now)
	if !ok {
		d.fail(ctx, e, &Error{Permanent: true, Status: "4.4.7", Msg: "delivery time expired; last error: " + de.Error()})
		return
	}
	d.log.Info("delivery deferred", "id", e.ID, "attempt", e.Attempts+1, "next", next, "err", de)
	if err := d.cfg.Queue.Retry(ctx, e.ID, next, de); err != nil {
		d.log.Error("queue retry failed", "id", e.ID, "err", err)
	}
}

func (d *Dispatcher) ack(ctx context.Context, e *queue.Entry) {
	if err := d.cfg.Queue.Ack(ctx, e.ID); err != nil {
		d.log.Error("queue ack failed", "id", e.ID, "err", err)
	}
}

// fail removes e and bounces it to its sender (never to a null sender).
func (d *Dispatcher) fail(ctx context.Context, e *queue.Entry, cause *Error) {
	d.log.Warn("delivery failed permanently", "id", e.ID, "err", cause)
	if e.Envelope.Sender != "" {
		if err := d.bounce(ctx, e, cause); err != nil {
			// Keep the entry: losing the bounce silently is worse than retrying.
			d.log.Error("cannot queue DSN, will retry", "id", e.ID, "err", err)
			if err := d.cfg.Queue.Retry(ctx, e.ID, d.cfg.Now().Add(d.cfg.Retry.Backoff(1)), err); err != nil {
				d.log.Error("queue retry failed", "id", e.ID, "err", err)
			}
			return
		}
	}
	if err := d.cfg.Queue.Fail(ctx, e.ID, cause); err != nil {
		d.log.Error("queue fail failed", "id", e.ID, "err", err)
	}
}

func (d *Dispatcher) bounce(ctx context.Context, e *queue.Entry, cause *Error) error {
	var headers []byte
	if body, err := d.cfg.Queue.Body(ctx, e.ID); err == nil {
		headers, _ = io.ReadAll(io.LimitReader(body, maxDSNHeaderBytes))
		body.Close()
	}
	msg, err := dsn.Build(dsn.Report{
		ReportingMTA:    d.cfg.Hostname,
		OriginalSender:  e.Envelope.Sender,
		FinalRecipient:  e.Envelope.Recipient,
		Action:          dsn.ActionFailed,
		Status:          cause.Status,
		Diagnostic:      cause.Error(),
		OriginalID:      e.ID,
		ArrivalDate:     e.ReceivedAt,
		LastAttempt:     d.cfg.Now(),
		OriginalHeaders: headers,
	}, d.cfg.Now())
	if err != nil {
		return fmt.Errorf("build DSN: %w", err)
	}
	action, err := json.Marshal([]policy.Action{{Kind: policy.KindForward, Targets: []string{e.Envelope.Sender}}})
	if err != nil {
		return err
	}
	ids, err := d.cfg.Queue.Enqueue(ctx, []queue.Envelope{{Sender: "", Recipient: e.Envelope.Sender, Action: action}}, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	d.log.Info("DSN queued", "id", e.ID, "dsn_id", ids[0])
	return nil
}
