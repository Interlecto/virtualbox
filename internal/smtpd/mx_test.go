package smtpd

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/interlecto/virtualbox/internal/policy"
	"github.com/interlecto/virtualbox/internal/queue"
)

type harness struct {
	mx *MX
	q  *queue.FileQueue
}

func (h *harness) addr() string { return h.mx.Addr().String() }

func newHarness(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	q, err := queue.OpenFileQueue(t.TempDir(), queue.FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Hostname: "mx.example.org",
		Addr:     "127.0.0.1:0",
		Resolver: policy.NewStaticResolver(map[string]string{
			"example.org": "boss@example.org",
			"closed.org":  "",
		}),
		Queue: q,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	mx, err := NewMX(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := mx.Listen(); err != nil {
		t.Fatal(err)
	}
	go mx.Serve()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		mx.Shutdown(ctx)
	})
	return &harness{mx: mx, q: q}
}

func dial(t *testing.T, h *harness) *smtp.Client {
	t.Helper()
	c, err := smtp.Dial(h.addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Hello("client.test"); err != nil {
		t.Fatal(err)
	}
	return c
}

func claim(t *testing.T, q queue.Queue) (*queue.Entry, string) {
	t.Helper()
	ctx := context.Background()
	e, err := q.Claim(ctx, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	r, err := q.Body(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	return e, string(b)
}

func assertQueueEmpty(t *testing.T, q queue.Queue) {
	t.Helper()
	if _, err := q.Claim(context.Background(), time.Now().Add(time.Minute)); !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("queue should be empty, got %v", err)
	}
}

func smtpErr(t *testing.T, err error) *smtp.SMTPError {
	t.Helper()
	var se *smtp.SMTPError
	if !errors.As(err, &se) {
		t.Fatalf("want *smtp.SMTPError, got %T: %v", err, err)
	}
	return se
}

const msg = "From: alice@remote.test\r\nTo: x@example.org\r\nSubject: hi\r\n\r\nhello\r\n"

func TestAcceptAndQueue(t *testing.T) {
	h := newHarness(t, nil)
	c := dial(t, h)

	if err := c.SendMail("alice@remote.test", []string{"x@example.org"}, strings.NewReader(msg)); err != nil {
		t.Fatal(err)
	}

	e, body := claim(t, h.q)
	if e.Envelope.Sender != "alice@remote.test" || e.Envelope.Recipient != "x@example.org" {
		t.Fatalf("envelope: %+v", e.Envelope)
	}
	var actions []policy.Action
	if err := json.Unmarshal(e.Envelope.Action, &actions); err != nil || len(actions) != 1 ||
		actions[0].Kind != policy.KindForward || actions[0].Targets[0] != "boss@example.org" {
		t.Fatalf("actions: %s (%v)", e.Envelope.Action, err)
	}
	if !strings.HasPrefix(body, "Received: from client.test ([127.0.0.1])\r\n") {
		t.Fatalf("missing Received header:\n%s", body)
	}
	if !strings.Contains(body, "by mx.example.org with ESMTP;") || !strings.HasSuffix(body, msg) {
		t.Fatalf("unexpected body:\n%s", body)
	}
}

func TestMultipleRecipientsShareBody(t *testing.T) {
	h := newHarness(t, nil)
	c := dial(t, h)
	if err := c.SendMail("alice@remote.test", []string{"a@example.org", "b@example.org", "A@Example.org"}, strings.NewReader(msg)); err != nil {
		t.Fatal(err)
	}
	e1, _ := claim(t, h.q)
	e2, _ := claim(t, h.q)
	if e1.BodyID != e2.BodyID || e1.Envelope.Recipient == e2.Envelope.Recipient {
		t.Fatalf("entries should share a body and differ in recipient: %+v %+v", e1, e2)
	}
	assertQueueEmpty(t, h.q) // the duplicate recipient was collapsed
}

func TestRejectsForeignDomain(t *testing.T) {
	h := newHarness(t, nil)
	c := dial(t, h)
	if err := c.Mail("spammer@evil.test", nil); err != nil {
		t.Fatal(err)
	}
	se := smtpErr(t, c.Rcpt("victim@gmail.example", nil))
	if se.Code != 550 || se.EnhancedCode != (smtp.EnhancedCode{5, 7, 1}) {
		t.Fatalf("want 550 5.7.1, got %+v", se)
	}
	assertQueueEmpty(t, h.q)
}

func TestRejectsUnknownUser(t *testing.T) {
	h := newHarness(t, nil)
	c := dial(t, h)
	if err := c.Mail("alice@remote.test", nil); err != nil {
		t.Fatal(err)
	}
	se := smtpErr(t, c.Rcpt("nobody@closed.org", nil))
	if se.Code != 550 || se.EnhancedCode != (smtp.EnhancedCode{5, 1, 1}) {
		t.Fatalf("want 550 5.1.1, got %+v", se)
	}
}

func TestMixedRecipientsDeliverToValidOnly(t *testing.T) {
	h := newHarness(t, nil)
	c := dial(t, h)
	if err := c.Mail("alice@remote.test", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt("ok@example.org", nil); err != nil {
		t.Fatal(err)
	}
	smtpErr(t, c.Rcpt("bad@elsewhere.test", nil))
	w, err := c.Data()
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, msg)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	e, _ := claim(t, h.q)
	if e.Envelope.Recipient != "ok@example.org" {
		t.Fatalf("recipient %q", e.Envelope.Recipient)
	}
	assertQueueEmpty(t, h.q)
}

func TestNullSender(t *testing.T) {
	h := newHarness(t, nil)
	c := dial(t, h)
	if err := c.SendMail("", []string{"x@example.org"}, strings.NewReader(msg)); err != nil {
		t.Fatal(err)
	}
	e, _ := claim(t, h.q)
	if e.Envelope.Sender != "" {
		t.Fatalf("sender %q, want null sender", e.Envelope.Sender)
	}
}

func TestMessageTooLarge(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxMessageBytes = 1024 })
	c := dial(t, h)
	big := msg + strings.Repeat("0123456789abcdef\r\n", 512)
	err := c.SendMail("alice@remote.test", []string{"x@example.org"}, strings.NewReader(big))
	if se := smtpErr(t, err); se.Code != 552 {
		t.Fatalf("want 552, got %+v", se)
	}
	assertQueueEmpty(t, h.q)
}

func TestMaxRecipients(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxRecipients = 2 })
	c := dial(t, h)
	if err := c.Mail("alice@remote.test", nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"a@example.org", "b@example.org"} {
		if err := c.Rcpt(r, nil); err != nil {
			t.Fatal(err)
		}
	}
	if se := smtpErr(t, c.Rcpt("c@example.org", nil)); se.Code != 452 {
		t.Fatalf("want 452, got %+v", se)
	}
}

func TestDataWithoutValidRecipient(t *testing.T) {
	h := newHarness(t, nil)
	c := dial(t, h)
	if err := c.Mail("alice@remote.test", nil); err != nil {
		t.Fatal(err)
	}
	smtpErr(t, c.Rcpt("bad@elsewhere.test", nil))
	w, err := c.Data()
	if err == nil {
		io.WriteString(w, msg)
		err = w.Close()
	}
	if err == nil {
		t.Fatal("DATA without a valid recipient must fail")
	}
	assertQueueEmpty(t, h.q)
}

func TestNoAuthAdvertised(t *testing.T) {
	h := newHarness(t, nil)
	c := dial(t, h)
	if ok, _ := c.Extension("AUTH"); ok {
		t.Fatal("MX port must not offer AUTH")
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		t.Fatal("STARTTLS advertised without a TLS config")
	}
}

type failingQueue struct{ queue.Queue }

func (failingQueue) Enqueue(context.Context, []queue.Envelope, io.Reader) ([]string, error) {
	return nil, errors.New("disk full")
}

func TestQueueFailureIsTemporary(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Queue = failingQueue{c.Queue} })
	c := dial(t, h)
	err := c.SendMail("alice@remote.test", []string{"x@example.org"}, strings.NewReader(msg))
	se := smtpErr(t, err)
	if se.Code != 451 {
		t.Fatalf("durability failure must be a 4xx so the sender retries, got %+v", se)
	}
}

type brokenResolver struct{}

func (brokenResolver) Resolve(context.Context, string) ([]policy.Action, error) {
	return nil, errors.New("database unavailable")
}

func TestResolverFailureIsTemporary(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Resolver = brokenResolver{} })
	c := dial(t, h)
	if err := c.Mail("alice@remote.test", nil); err != nil {
		t.Fatal(err)
	}
	if se := smtpErr(t, c.Rcpt("x@example.org", nil)); se.Code != 451 {
		t.Fatalf("want 451, got %+v", se)
	}
}

func selfSigned(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mx.example.org"},
		DNSNames:     []string{"mx.example.org"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
}

func TestSTARTTLS(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.TLS = selfSigned(t) })

	plain := dial(t, h)
	if ok, _ := plain.Extension("STARTTLS"); !ok {
		t.Fatal("STARTTLS not advertised")
	}

	c, err := smtp.DialStartTLS(h.addr(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, ok := c.TLSConnectionState(); !ok {
		t.Fatal("connection is not using TLS")
	}
	if err := c.SendMail("alice@remote.test", []string{"x@example.org"}, strings.NewReader(msg)); err != nil {
		t.Fatal(err)
	}
	_, body := claim(t, h.q)
	if !strings.Contains(body, "with ESMTPS;") {
		t.Fatalf("Received header should record TLS:\n%s", body)
	}
}

func greeting(t *testing.T, addr string) (net.Conn, string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	return conn, line
}

func TestConnectionLimits(t *testing.T) {
	cases := map[string]func(*Config){
		"total":  func(c *Config) { c.MaxConnections = 1; c.MaxConnectionsPerIP = 50 },
		"per-ip": func(c *Config) { c.MaxConnections = 50; c.MaxConnectionsPerIP = 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, mutate)

			first, line := greeting(t, h.addr())
			if !strings.HasPrefix(line, "220 ") {
				t.Fatalf("first connection: %q", line)
			}
			second, line := greeting(t, h.addr())
			second.Close()
			if !strings.HasPrefix(line, "421 ") {
				t.Fatalf("second connection should be refused with 421, got %q", line)
			}

			// Freeing the slot lets new connections in again.
			first.Close()
			deadline := time.Now().Add(2 * time.Second)
			for {
				conn, line := greeting(t, h.addr())
				conn.Close()
				if strings.HasPrefix(line, "220 ") {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("slot never released, last reply %q", line)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

func TestNewMXValidation(t *testing.T) {
	q, _ := queue.OpenFileQueue(t.TempDir(), queue.FileOptions{})
	res := policy.NewStaticResolver(nil)
	for name, cfg := range map[string]Config{
		"no hostname": {Resolver: res, Queue: q},
		"no resolver": {Hostname: "h", Queue: q},
		"no queue":    {Hostname: "h", Resolver: res},
	} {
		if _, err := NewMX(cfg); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
