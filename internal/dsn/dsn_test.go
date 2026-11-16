package dsn

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func baseReport() Report {
	return Report{
		ReportingMTA:    "mx.example.org",
		OriginalSender:  "alice@example.com",
		FinalRecipient:  "bob@remote.test",
		Action:          ActionFailed,
		Status:          "5.1.1",
		Diagnostic:      "550 5.1.1 user unknown",
		OriginalID:      "abc123",
		ArrivalDate:     now.Add(-48 * time.Hour),
		LastAttempt:     now,
		OriginalHeaders: []byte("From: alice@example.com\nTo: bob@remote.test\nSubject: hello\n\nsecret body"),
	}
}

func parts(t *testing.T, raw []byte) (*mail.Message, []string, []string) {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/report" || params["report-type"] != "delivery-status" {
		t.Fatalf("content type %q %v %v", mt, params, err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var types, bodies []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		types = append(types, p.Header.Get("Content-Type"))
		bodies = append(bodies, string(b))
	}
	return m, types, bodies
}

func TestBuildFailed(t *testing.T) {
	raw, err := Build(baseReport(), now)
	if err != nil {
		t.Fatal(err)
	}
	m, types, bodies := parts(t, raw)

	if got := m.Header.Get("To"); got != "<alice@example.com>" {
		t.Errorf("To = %q", got)
	}
	if m.Header.Get("Auto-Submitted") != "auto-replied" {
		t.Error("missing Auto-Submitted")
	}
	if !strings.Contains(m.Header.Get("From"), "MAILER-DAEMON@mx.example.org") {
		t.Errorf("From = %q", m.Header.Get("From"))
	}
	if len(types) != 3 {
		t.Fatalf("want 3 parts, got %v", types)
	}
	if !strings.HasPrefix(types[1], "message/delivery-status") {
		t.Errorf("part 2 type %q", types[1])
	}
	status := bodies[1]
	for _, want := range []string{
		"Reporting-MTA: dns; mx.example.org",
		"Final-Recipient: rfc822; bob@remote.test",
		"Action: failed",
		"Status: 5.1.1",
		"Diagnostic-Code: smtp; 550 5.1.1 user unknown",
	} {
		if !strings.Contains(status, want) {
			t.Errorf("delivery-status lacks %q:\n%s", want, status)
		}
	}
	if !strings.Contains(bodies[2], "Subject: hello") {
		t.Errorf("original headers missing: %q", bodies[2])
	}
	if strings.Contains(string(raw), "secret body") {
		t.Error("original body must never be quoted")
	}
}

func TestBuildDelayed(t *testing.T) {
	r := baseReport()
	r.Action, r.Status = ActionDelayed, "4.4.7"
	raw, err := Build(r, now)
	if err != nil {
		t.Fatal(err)
	}
	m, _, bodies := parts(t, raw)
	if !strings.Contains(m.Header.Get("Subject"), "Delay") {
		t.Errorf("Subject = %q", m.Header.Get("Subject"))
	}
	if !strings.Contains(bodies[1], "Action: delayed") {
		t.Errorf("status part: %s", bodies[1])
	}
}

func TestBuildErrors(t *testing.T) {
	cases := map[string]func(*Report){
		"bad status":   func(r *Report) { r.Status = "9.9" },
		"bad action":   func(r *Report) { r.Action = "relayed" },
		"no mta":       func(r *Report) { r.ReportingMTA = "" },
		"no recipient": func(r *Report) { r.FinalRecipient = "" },
	}
	for name, mutate := range cases {
		r := baseReport()
		mutate(&r)
		if _, err := Build(r, now); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	r := baseReport()
	r.OriginalSender = ""
	if _, err := Build(r, now); !errors.Is(err, ErrNullSender) {
		t.Errorf("null sender: want ErrNullSender, got %v", err)
	}
}

func TestBuildNoHeaderInjection(t *testing.T) {
	r := baseReport()
	r.OriginalSender = "alice@example.com\r\nBcc: evil@example.net"
	r.Diagnostic = "boom\r\nX-Injected: yes"
	raw, err := Build(r, now)
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Header.Get("Bcc") != "" {
		t.Error("Bcc header injected through sender")
	}
	if strings.Contains(string(raw), "\r\nX-Injected:") {
		t.Error("header injected through diagnostic")
	}
}
