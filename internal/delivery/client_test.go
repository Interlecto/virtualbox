package delivery

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
)

// --- fake SMTP server ---

type received struct {
	from, to, body string
	tls            bool
}

type fakeServer struct {
	mu       sync.Mutex
	msgs     []received
	rcptErr  *smtp.SMTPError // reply to RCPT, nil = accept
	greetErr bool
	srv      *smtp.Server
	ln       net.Listener
}

func (f *fakeServer) NewSession(c *smtp.Conn) (smtp.Session, error) {
	if f.greetErr {
		return nil, &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 3, 2}, Message: "go away"}
	}
	_, isTLS := c.TLSConnectionState()
	return &fakeSession{f: f, tlsFn: func() bool { _, ok := c.TLSConnectionState(); return ok || isTLS }}, nil
}

type fakeSession struct {
	f        *fakeServer
	tlsFn    func() bool
	from, to string
}

func (s *fakeSession) AuthPlain(string, string) error { return smtp.ErrAuthUnsupported }
func (s *fakeSession) Mail(from string, _ *smtp.MailOptions) error {
	s.from = from
	return nil
}
func (s *fakeSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if s.f.rcptErr != nil {
		return s.f.rcptErr
	}
	s.to = to
	return nil
}
func (s *fakeSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.f.mu.Lock()
	s.f.msgs = append(s.f.msgs, received{from: s.from, to: s.to, body: string(b), tls: s.tlsFn()})
	s.f.mu.Unlock()
	return nil
}
func (s *fakeSession) Reset()        {}
func (s *fakeSession) Logout() error { return nil }

func startFake(t *testing.T, withTLS bool) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	f.srv = smtp.NewServer(f)
	f.srv.Domain = "fake.test"
	f.srv.AllowInsecureAuth = true
	if withTLS {
		f.srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	go f.srv.Serve(ln)
	t.Cleanup(func() { f.srv.Close() })
	return f
}

func (f *fakeServer) got() []received {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]received(nil), f.msgs...)
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fake.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"fake.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// --- fake DNS ---

type fakeDNS struct {
	mx    map[string][]*net.MX
	hosts map[string]bool
	fail  bool
}

func (d *fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if d.fail {
		return nil, &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
	}
	if mx, ok := d.mx[name]; ok {
		return mx, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (d *fakeDNS) LookupHost(_ context.Context, host string) ([]string, error) {
	if d.hosts[host] {
		return []string{"127.0.0.1"}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// newClient routes host names to fake servers; unknown hosts refuse.
func newClient(dns *fakeDNS, routes map[string]*fakeServer) *Client {
	return &Client{
		Hostname: "mx.virtualbox.test",
		Resolver: dns,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			f, ok := routes[host]
			if !ok {
				return nil, errors.New("connection refused")
			}
			var d net.Dialer
			return d.DialContext(ctx, network, f.ln.Addr().String())
		},
		SessionTimeout: 10 * time.Second,
	}
}

func body(s string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(s)), nil }
}

const msg = "Subject: hi\r\n\r\nhello\r\n.dot line\r\n"

func TestClientDeliversWithSTARTTLSAndFailover(t *testing.T) {
	good := startFake(t, true)
	dns := &fakeDNS{mx: map[string][]*net.MX{"dest.test": {
		{Host: "down.dest.test.", Pref: 5},
		{Host: "mx.dest.test.", Pref: 10},
	}}}
	c := newClient(dns, map[string]*fakeServer{"mx.dest.test": good})

	if err := c.Send(context.Background(), "a@src.test", "bob@dest.test", body(msg)); err != nil {
		t.Fatal(err)
	}
	got := good.got()
	if len(got) != 1 || got[0].from != "a@src.test" || got[0].to != "bob@dest.test" || !got[0].tls {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got[0].body, ".dot line") {
		t.Fatalf("dot-stuffing broken: %q", got[0].body)
	}
}

func TestClientImplicitMXAndNullSender(t *testing.T) {
	f := startFake(t, false)
	dns := &fakeDNS{hosts: map[string]bool{"plain.test": true}}
	c := newClient(dns, map[string]*fakeServer{"plain.test": f})
	if err := c.Send(context.Background(), "", "x@plain.test", body(msg)); err != nil {
		t.Fatal(err)
	}
	if got := f.got(); len(got) != 1 || got[0].from != "" || got[0].tls {
		t.Fatalf("got %+v", got)
	}
}

func TestClientErrors(t *testing.T) {
	reject := startFake(t, false)
	reject.rcptErr = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user"}
	busy := startFake(t, false)
	busy.rcptErr = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "greylisted"}
	grumpy := startFake(t, false)
	grumpy.greetErr = true

	dns := &fakeDNS{
		mx: map[string][]*net.MX{
			"reject.test": {{Host: "r.test", Pref: 1}},
			"busy.test":   {{Host: "b.test", Pref: 1}},
			"grumpy.test": {{Host: "g.test", Pref: 1}},
			"null.test":   {{Host: ".", Pref: 0}},
			"loop.test":   {{Host: "MX.virtualbox.test.", Pref: 1}},
			"down.test":   {{Host: "nowhere.test", Pref: 1}},
		},
	}
	c := newClient(dns, map[string]*fakeServer{"r.test": reject, "b.test": busy, "g.test": grumpy})

	cases := []struct {
		rcpt   string
		perm   bool
		status string
	}{
		{"u@reject.test", true, "5.1.1"},
		{"u@busy.test", false, "4.7.1"},
		{"u@grumpy.test", false, "4.0.0"}, // 5xx greeting: host-level, temporary
		{"u@null.test", true, "5.1.10"},
		{"u@loop.test", true, "5.4.6"},
		{"u@down.test", false, "4.4.1"},
		{"u@nxdomain.test", true, "5.1.2"},
		{"garbage", true, "5.1.3"},
	}
	for _, tc := range cases {
		err := c.Send(context.Background(), "a@src.test", tc.rcpt, body(msg))
		var de *Error
		if !errors.As(err, &de) {
			t.Errorf("%s: err = %v", tc.rcpt, err)
			continue
		}
		if de.Permanent != tc.perm || de.Status != tc.status {
			t.Errorf("%s: got permanent=%v status=%s (%v), want %v %s", tc.rcpt, de.Permanent, de.Status, de, tc.perm, tc.status)
		}
	}

	dns.fail = true
	err := c.Send(context.Background(), "a@src.test", "u@reject.test", body(msg))
	var de *Error
	if !errors.As(err, &de) || de.Permanent || de.Status != "4.4.3" {
		t.Fatalf("DNS failure: %v", err)
	}
}
