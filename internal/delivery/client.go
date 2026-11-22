package delivery

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/smtp"
	"net/textproto"
	"regexp"
	"strings"
	"time"
)

// Error is a classified delivery failure.
type Error struct {
	// Permanent failures are not retried and produce a DSN right away.
	Permanent bool
	// Status is the RFC 3463 enhanced status code, e.g. "5.1.1" or "4.4.1".
	Status string
	// Msg is the diagnostic (remote reply or local reason).
	Msg string
	// Host is the remote host that produced the reply, if any.
	Host string
}

func (e *Error) Error() string {
	if e.Host != "" {
		return fmt.Sprintf("%s: %s %s", e.Host, e.Status, e.Msg)
	}
	return e.Status + " " + e.Msg
}

func permanent(status, format string, args ...any) *Error {
	return &Error{Permanent: true, Status: status, Msg: fmt.Sprintf(format, args...)}
}

func temporary(status, format string, args ...any) *Error {
	return &Error{Status: status, Msg: fmt.Sprintf(format, args...)}
}

// Sender delivers one message to one recipient. Client is the SMTP
// implementation; tests and later steps (smarthost, 2.5) can swap it.
type Sender interface {
	// Send delivers the body returned by open (it may be called more than
	// once, e.g. once per MX host tried) from sender (empty = null sender)
	// to rcpt. Failures should be *Error so they can be classified; any
	// other error is treated as temporary.
	Send(ctx context.Context, sender, rcpt string, open func() (io.ReadCloser, error)) error
}

// MXResolver is the subset of *net.Resolver the client needs.
type MXResolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Client delivers mail directly to the recipient domain's MX hosts.
//
// Hosts are tried in MX preference order (implicit MX on the domain itself
// when it has none, RFC 5321 5.1; a null MX, RFC 7505, fails permanently).
// Connection problems and 4xx replies move on to the next host; a 5xx reply
// to MAIL, RCPT or DATA is permanent. STARTTLS is used whenever offered
// (opportunistic, RFC 7435: certificates are not verified until MTA-STS and
// DANE land in roadmap 6.2).
type Client struct {
	// Hostname is announced in EHLO. It is also used to detect MX records
	// pointing back at this server (mail loop).
	Hostname string
	// Resolver defaults to net.DefaultResolver.
	Resolver MXResolver
	// Port defaults to "25". Tests use another one.
	Port string
	// DialContext defaults to a net.Dialer with DialTimeout.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// DialTimeout defaults to 30s; SessionTimeout (whole SMTP dialogue with
	// one host) defaults to 10 minutes.
	DialTimeout    time.Duration
	SessionTimeout time.Duration
	// MaxHosts bounds how many MX hosts are tried per attempt (default 5).
	MaxHosts int
	// TLSConfig is cloned for STARTTLS; ServerName is set per host.
	// Nil means opportunistic TLS 1.2+ without certificate verification.
	TLSConfig *tls.Config
	Logger    *slog.Logger
}

var _ Sender = (*Client)(nil)

func (c *Client) resolver() MXResolver {
	if c.Resolver != nil {
		return c.Resolver
	}
	return net.DefaultResolver
}

func (c *Client) log() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Send implements Sender.
func (c *Client) Send(ctx context.Context, sender, rcpt string, open func() (io.ReadCloser, error)) error {
	i := strings.LastIndexByte(rcpt, '@')
	if i <= 0 || i == len(rcpt)-1 {
		return permanent("5.1.3", "malformed recipient address %q", rcpt)
	}
	domain := strings.ToLower(strings.TrimSuffix(rcpt[i+1:], "."))

	hosts, err := c.lookupHosts(ctx, domain)
	if err != nil {
		return err
	}
	max := c.MaxHosts
	if max <= 0 {
		max = 5
	}
	if len(hosts) > max {
		hosts = hosts[:max]
	}

	var last *Error
	for _, h := range hosts {
		if strings.EqualFold(strings.TrimSuffix(h, "."), strings.TrimSuffix(c.Hostname, ".")) {
			return permanent("5.4.6", "MX for %s points back to this server (mail loop)", domain)
		}
		err := c.sendHost(ctx, h, sender, rcpt, open)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return temporary("4.4.2", "delivery interrupted: %v", ctx.Err())
		}
		last = err
		if err.Permanent {
			return err
		}
		c.log().Info("delivery attempt failed, trying next host", "host", h, "err", err)
	}
	if last == nil {
		last = temporary("4.4.4", "no usable MX host for %s", domain)
	}
	return last
}

// lookupHosts returns the hosts to try for domain, best first.
func (c *Client) lookupHosts(ctx context.Context, domain string) ([]string, error) {
	mxs, err := c.resolver().LookupMX(ctx, domain)
	if err != nil {
		if !isNotFound(err) {
			return nil, temporary("4.4.3", "MX lookup for %s failed: %v", domain, err)
		}
		mxs = nil
	}
	if len(mxs) == 1 && (mxs[0].Host == "." || mxs[0].Host == "") {
		return nil, permanent("5.1.10", "domain %s does not accept mail (null MX)", domain)
	}
	hosts := make([]string, 0, len(mxs))
	for _, mx := range mxs {
		if h := strings.TrimSuffix(mx.Host, "."); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) > 0 {
		return hosts, nil // net.Resolver already sorts by preference
	}
	// Implicit MX: the domain's own address records.
	if _, err := c.resolver().LookupHost(ctx, domain); err != nil {
		if isNotFound(err) {
			return nil, permanent("5.1.2", "domain %s not found", domain)
		}
		return nil, temporary("4.4.3", "address lookup for %s failed: %v", domain, err)
	}
	return []string{domain}, nil
}

func isNotFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

// sendHost runs one SMTP transaction with host.
func (c *Client) sendHost(ctx context.Context, host, sender, rcpt string, open func() (io.ReadCloser, error)) (result *Error) {
	port := c.Port
	if port == "" {
		port = "25"
	}
	dial := c.DialContext
	if dial == nil {
		timeout := c.DialTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		d := &net.Dialer{Timeout: timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return &Error{Status: "4.4.1", Msg: "connection failed: " + err.Error(), Host: host}
	}
	session := c.SessionTimeout
	if session <= 0 {
		session = 10 * time.Minute
	}
	_ = conn.SetDeadline(time.Now().Add(session))
	// Abort the dialogue promptly on shutdown.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	// stage tells how to classify a failure: greeting/EHLO/TLS problems are
	// host-level (try the next MX), MAIL/RCPT/DATA replies are definitive.
	stage := "connect"
	fail := func(err error) *Error {
		e := classify(err, stage != "transaction")
		e.Host = host
		return e
	}

	cl, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fail(err)
	}
	defer cl.Close()

	stage = "ehlo"
	if err := cl.Hello(c.Hostname); err != nil {
		return fail(err)
	}
	if ok, _ := cl.Extension("STARTTLS"); ok {
		stage = "starttls"
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // opportunistic TLS, see Client doc
		if c.TLSConfig != nil {
			cfg = c.TLSConfig.Clone()
		}
		cfg.ServerName = host
		if err := cl.StartTLS(cfg); err != nil {
			return fail(err)
		}
	}

	stage = "transaction"
	if err := cl.Mail(sender); err != nil {
		return fail(err)
	}
	if err := cl.Rcpt(rcpt); err != nil {
		return fail(err)
	}
	body, err := open()
	if err != nil {
		cl.Reset()
		return &Error{Status: "4.3.0", Msg: "cannot read queued message: " + err.Error()}
	}
	defer body.Close()
	w, err := cl.Data()
	if err != nil {
		return fail(err)
	}
	if _, err := io.Copy(w, body); err != nil {
		return fail(err)
	}
	if err := w.Close(); err != nil {
		return fail(err)
	}
	_ = cl.Quit()
	return nil
}

var enhancedRe = regexp.MustCompile(`^([245])\.(\d{1,3})\.(\d{1,3})\b`)

// classify turns an SMTP or network error into an *Error. hostLevel errors
// (before MAIL FROM) are always temporary so the next MX gets a chance.
func classify(err error, hostLevel bool) *Error {
	var pe *textproto.Error
	if errors.As(err, &pe) {
		msg := strings.TrimSpace(pe.Msg)
		status := ""
		if m := enhancedRe.FindString(msg); m != "" {
			status = m
			msg = strings.TrimSpace(msg[len(m):])
		}
		perm := pe.Code >= 500 && !hostLevel
		if status == "" || (perm && status[0] != '5') || (!perm && status[0] == '5') {
			if perm {
				status = "5.0.0"
			} else {
				status = "4.0.0"
			}
		}
		return &Error{Permanent: perm, Status: status, Msg: fmt.Sprintf("%d %s", pe.Code, msg)}
	}
	return &Error{Status: "4.4.2", Msg: err.Error()}
}
