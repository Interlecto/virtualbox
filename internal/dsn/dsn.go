// Package dsn builds Delivery Status Notifications (bounces) per RFC 3464.
//
// Build only produces the message bytes. The caller must enqueue the result
// with a NULL envelope sender (MAIL FROM:<>) so a bounce can never bounce
// again, and must not generate DSNs for messages whose sender is already null.
package dsn

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Action values defined by RFC 3464 that this server generates.
const (
	ActionFailed  = "failed"  // permanent failure, delivery abandoned
	ActionDelayed = "delayed" // temporary failure, still retrying
)

// maxHeaderBytes limits how much of the original message is quoted.
const maxHeaderBytes = 16 * 1024

// ErrNullSender is returned when the original sender is empty: DSNs must
// never be sent in answer to a null sender.
var ErrNullSender = errors.New("dsn: original sender is null")

var statusRe = regexp.MustCompile(`^[245]\.\d{1,3}\.\d{1,3}$`)

// Report describes one failed or delayed delivery to one recipient.
type Report struct {
	// ReportingMTA is this server's hostname.
	ReportingMTA string
	// OriginalSender is the envelope sender of the failed message; the DSN
	// is addressed to it.
	OriginalSender string
	// FinalRecipient is the recipient that could not be reached.
	FinalRecipient string
	// Action is ActionFailed or ActionDelayed.
	Action string
	// Status is the enhanced status code, e.g. "5.1.1" or "4.4.7".
	Status string
	// Diagnostic is the human-readable reason (remote reply, timeout, ...).
	Diagnostic string
	// OriginalID is the queue entry ID, for operator reference.
	OriginalID string
	// ArrivalDate is when the original message was accepted.
	ArrivalDate time.Time
	// LastAttempt is when delivery was last tried.
	LastAttempt time.Time
	// OriginalHeaders optionally holds the original message; only its header
	// section (truncated) is quoted, never the body.
	OriginalHeaders []byte
}

// Build renders the DSN as an RFC 5322 message with CRLF line endings.
// now is used for the Date header.
func Build(r Report, now time.Time) ([]byte, error) {
	if strings.TrimSpace(r.OriginalSender) == "" {
		return nil, ErrNullSender
	}
	if r.ReportingMTA == "" || r.FinalRecipient == "" {
		return nil, errors.New("dsn: ReportingMTA and FinalRecipient are required")
	}
	if r.Action != ActionFailed && r.Action != ActionDelayed {
		return nil, fmt.Errorf("dsn: unsupported action %q", r.Action)
	}
	if !statusRe.MatchString(r.Status) {
		return nil, fmt.Errorf("dsn: invalid status %q", r.Status)
	}

	boundary, err := randomHex(12)
	if err != nil {
		return nil, err
	}
	msgID, err := randomHex(12)
	if err != nil {
		return nil, err
	}

	host := clean(r.ReportingMTA)
	rcpt := clean(r.FinalRecipient)
	subject := "Undelivered Mail Returned to Sender"
	summary := "Your message could not be delivered to " + rcpt + "."
	if r.Action == ActionDelayed {
		subject = "Delivery Status Notification (Delay)"
		summary = "Delivery of your message to " + rcpt + " has been delayed. The server will keep trying."
	}

	var b bytes.Buffer
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\r\n", args...)
	}

	line("From: Mail Delivery System <MAILER-DAEMON@%s>", host)
	line("To: <%s>", clean(r.OriginalSender))
	line("Subject: %s", subject)
	line("Date: %s", now.UTC().Format(time.RFC1123Z))
	line("Message-ID: <%s@%s>", msgID, host)
	line("Auto-Submitted: auto-replied")
	line("MIME-Version: 1.0")
	line("Content-Type: multipart/report; report-type=delivery-status;")
	line(` boundary="%s"`, boundary)
	line("")

	// Part 1: human readable.
	line("--%s", boundary)
	line("Content-Type: text/plain; charset=utf-8")
	line("Content-Transfer-Encoding: 8bit")
	line("")
	line("%s", summary)
	line("")
	line("Reason: %s", clean(r.Diagnostic))
	if r.OriginalID != "" {
		line("Reference: %s", clean(r.OriginalID))
	}
	line("")

	// Part 2: machine readable.
	line("--%s", boundary)
	line("Content-Type: message/delivery-status")
	line("")
	line("Reporting-MTA: dns; %s", host)
	if !r.ArrivalDate.IsZero() {
		line("Arrival-Date: %s", r.ArrivalDate.UTC().Format(time.RFC1123Z))
	}
	line("")
	line("Final-Recipient: rfc822; %s", rcpt)
	line("Action: %s", r.Action)
	line("Status: %s", r.Status)
	if r.Diagnostic != "" {
		line("Diagnostic-Code: smtp; %s", clean(r.Diagnostic))
	}
	if !r.LastAttempt.IsZero() {
		line("Last-Attempt-Date: %s", r.LastAttempt.UTC().Format(time.RFC1123Z))
	}
	line("")

	// Part 3: original headers only.
	if h := headerSection(r.OriginalHeaders); h != "" {
		line("--%s", boundary)
		line("Content-Type: text/rfc822-headers; charset=utf-8")
		line("")
		b.WriteString(h)
		line("")
	}

	line("--%s--", boundary)
	return b.Bytes(), nil
}

// headerSection returns the header block of msg (up to the first blank
// line), truncated and normalised to CRLF, ending with a CRLF.
func headerSection(msg []byte) string {
	if len(msg) == 0 {
		return ""
	}
	s := strings.ReplaceAll(string(msg), "\r\n", "\n")
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > maxHeaderBytes {
		s = s[:maxHeaderBytes]
	}
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	return strings.ReplaceAll(s, "\n", "\r\n") + "\r\n"
}

// clean prevents header and field injection by flattening line breaks.
func clean(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(strings.TrimSpace(s))
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("dsn: %w", err)
	}
	return hex.EncodeToString(b), nil
}
