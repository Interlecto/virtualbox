// Package smtpd implements the inbound SMTP service (MX, port 25).
//
// The MX server accepts mail only for registered domains: recipients are
// validated during the SMTP session through a policy.Resolver and anything
// unknown is rejected with a 5xx (never accepted and bounced later, which
// would create backscatter). It never relays for third parties.
//
// A message is acknowledged with "250" only after queue.Queue.Enqueue has
// returned, i.e. after it is durably stored.
package smtpd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/interlecto/virtualbox/internal/policy"
	"github.com/interlecto/virtualbox/internal/queue"
)

// Defaults applied to zero Config fields.
const (
	DefaultMaxMessageBytes     = 25 << 20 // 25 MiB
	DefaultMaxRecipients       = 100
	DefaultMaxConnections      = 200
	DefaultMaxConnectionsPerIP = 10
	DefaultTimeout             = 2 * time.Minute

	resolveTimeout = 10 * time.Second
)

// Config configures an MX server.
type Config struct {
	// Hostname is announced in the greeting and in Received headers.
	Hostname string
	// Addr is the listen address, normally ":25".
	Addr string
	// TLS enables STARTTLS when non-nil.
	TLS *tls.Config
	// Resolver validates recipients and decides their actions.
	Resolver policy.Resolver
	// Queue stores accepted messages.
	Queue queue.Queue
	// Logger receives operational logs; nil discards them.
	Logger *slog.Logger

	// Limits; zero selects the defaults above.
	MaxMessageBytes     int64
	MaxRecipients       int
	MaxConnections      int
	MaxConnectionsPerIP int
	// Timeout is both the read and write timeout of a connection.
	Timeout time.Duration
}

// MX is an inbound SMTP server.
type MX struct {
	cfg Config
	log *slog.Logger
	srv *smtp.Server
	ln  *limitListener
}

// NewMX validates cfg, applies defaults and builds the server. Call Listen
// and then Serve to run it.
func NewMX(cfg Config) (*MX, error) {
	if cfg.Hostname == "" {
		return nil, errors.New("smtpd: Hostname is required")
	}
	if cfg.Resolver == nil || cfg.Queue == nil {
		return nil, errors.New("smtpd: Resolver and Queue are required")
	}
	if cfg.MaxMessageBytes <= 0 {
		cfg.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if cfg.MaxRecipients <= 0 {
		cfg.MaxRecipients = DefaultMaxRecipients
	}
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = DefaultMaxConnections
	}
	if cfg.MaxConnectionsPerIP <= 0 {
		cfg.MaxConnectionsPerIP = DefaultMaxConnectionsPerIP
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	m := &MX{cfg: cfg, log: log}
	srv := smtp.NewServer(&backend{mx: m})
	srv.Domain = cfg.Hostname
	srv.Addr = cfg.Addr
	srv.TLSConfig = cfg.TLS
	srv.MaxMessageBytes = cfg.MaxMessageBytes
	srv.MaxRecipients = cfg.MaxRecipients
	srv.ReadTimeout = cfg.Timeout
	srv.WriteTimeout = cfg.Timeout
	// No AUTH: this port only receives mail for local domains. Submission
	// (authenticated sending) is a separate service.
	srv.AllowInsecureAuth = false
	srv.ErrorLog = slogPrinter{log}
	m.srv = srv
	return m, nil
}

// Listen binds the configured address.
func (m *MX) Listen() error {
	l, err := net.Listen("tcp", m.cfg.Addr)
	if err != nil {
		return fmt.Errorf("smtpd: %w", err)
	}
	m.ln = newLimitListener(l, m.cfg.MaxConnections, m.cfg.MaxConnectionsPerIP)
	return nil
}

// Addr returns the bound address; valid after Listen.
func (m *MX) Addr() net.Addr { return m.ln.Addr() }

// Serve accepts connections until Shutdown or Close. It returns nil after a
// clean shutdown.
func (m *MX) Serve() error {
	if m.ln == nil {
		return errors.New("smtpd: Serve called before Listen")
	}
	m.log.Info("mx listening", "addr", m.ln.Addr().String(), "tls", m.cfg.TLS != nil)
	err := m.srv.Serve(m.ln)
	if errors.Is(err, smtp.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops accepting connections and waits for active sessions to end
// or ctx to expire, in which case remaining connections are closed.
func (m *MX) Shutdown(ctx context.Context) error {
	err := m.srv.Shutdown(ctx)
	if err != nil {
		_ = m.srv.Close()
	}
	return err
}

type backend struct{ mx *MX }

func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &session{mx: b.mx, conn: c}, nil
}

type rcpt struct {
	addr    string
	actions []policy.Action
}

type session struct {
	mx    *MX
	conn  *smtp.Conn
	from  string
	rcpts []rcpt
}

func (s *session) Reset() {
	s.from = ""
	s.rcpts = nil
}

func (s *session) Logout() error { return nil }

func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	s.from = from // empty is the null sender (bounces), which is valid
	return nil
}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	for _, r := range s.rcpts {
		if strings.EqualFold(r.addr, to) {
			return nil // duplicate recipient in the same transaction
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	actions, err := s.mx.cfg.Resolver.Resolve(ctx, to)
	switch {
	case err == nil:
	case errors.Is(err, policy.ErrUnknownDomain):
		// We are not a relay for third-party domains.
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "Relay access denied"}
	case errors.Is(err, policy.ErrUnknownRecipient):
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "User unknown"}
	default:
		s.mx.log.Error("recipient resolution failed", "err", err)
		return tempFailure("Temporary resolution failure, try again later")
	}

	s.rcpts = append(s.rcpts, rcpt{addr: to, actions: actions})
	return nil
}

func (s *session) Data(r io.Reader) error {
	if len(s.rcpts) == 0 {
		return &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 5, 1}, Message: "No valid recipients"}
	}

	envs := make([]queue.Envelope, len(s.rcpts))
	for i, rc := range s.rcpts {
		action, err := json.Marshal(rc.actions)
		if err != nil {
			s.mx.log.Error("encode actions", "err", err)
			return tempFailure("Local error, try again later")
		}
		envs[i] = queue.Envelope{Sender: s.from, Recipient: rc.addr, Action: action}
	}

	body := io.MultiReader(strings.NewReader(s.receivedHeader()), r)
	ctx, cancel := context.WithTimeout(context.Background(), s.mx.cfg.Timeout)
	defer cancel()
	ids, err := s.mx.cfg.Queue.Enqueue(ctx, envs, body)
	if err != nil {
		// The size limit surfaces as an SMTPError from the data reader.
		var smtpErr *smtp.SMTPError
		if errors.As(err, &smtpErr) {
			return smtpErr
		}
		s.mx.log.Error("enqueue failed", "err", err)
		return tempFailure("Local error, try again later")
	}

	s.mx.log.Info("message accepted",
		"from", s.from, "recipients", len(envs), "queue_ids", ids, "remote", s.remote())
	return nil
}

func (s *session) remote() string {
	return remoteIP(s.conn.Conn().RemoteAddr())
}

// receivedHeader builds the trace header (RFC 5321 4.4) prepended to every
// accepted message.
func (s *session) receivedHeader() string {
	proto := "ESMTP"
	if _, ok := s.conn.TLSConnectionState(); ok {
		proto = "ESMTPS"
	}
	helo := flatten(s.conn.Hostname())
	if helo == "" {
		helo = "unknown"
	}
	return fmt.Sprintf("Received: from %s ([%s])\r\n\tby %s with %s;\r\n\t%s\r\n",
		helo, s.remote(), flatten(s.mx.cfg.Hostname), proto,
		time.Now().Format(time.RFC1123Z))
}

func tempFailure(msg string) *smtp.SMTPError {
	return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: msg}
}

// flatten removes line breaks so untrusted values cannot inject headers.
func flatten(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(strings.TrimSpace(s))
}

// slogPrinter adapts slog to go-smtp's Logger interface.
type slogPrinter struct{ l *slog.Logger }

func (p slogPrinter) Printf(format string, v ...interface{}) {
	p.l.Warn(fmt.Sprintf(format, v...))
}

func (p slogPrinter) Println(v ...interface{}) {
	p.l.Warn(strings.TrimSuffix(fmt.Sprintln(v...), "\n"))
}
