package queue

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	p := RetryPolicy{Initial: 5 * time.Minute, Max: time.Hour, MaxAge: 24 * time.Hour}
	want := map[int]time.Duration{
		1:   5 * time.Minute,
		2:   10 * time.Minute,
		3:   20 * time.Minute,
		4:   40 * time.Minute,
		5:   time.Hour,
		6:   time.Hour,
		100: time.Hour,
	}
	for attempts, d := range want {
		if got := p.Backoff(attempts); got != d {
			t.Errorf("Backoff(%d) = %v, want %v", attempts, got, d)
		}
	}
}

func TestNextAndExpiry(t *testing.T) {
	p := RetryPolicy{Initial: time.Hour, Max: 4 * time.Hour, MaxAge: 10 * time.Hour}
	e := Entry{ReceivedAt: t0, Attempts: 0}

	next, ok := p.Next(e, t0.Add(time.Minute))
	if !ok || !next.Equal(t0.Add(time.Minute+time.Hour)) {
		t.Fatalf("first retry: %v %v", next, ok)
	}

	e.Attempts = 3 // backoff 4h (capped)
	if _, ok := p.Next(e, t0.Add(8*time.Hour)); ok {
		t.Fatal("next attempt past MaxAge should report expiry")
	}
	if _, ok := p.Next(e, t0.Add(10*time.Hour)); ok {
		t.Fatal("expired entry should not be retried")
	}
	if !p.Expired(e, t0.Add(10*time.Hour)) || p.Expired(e, t0.Add(9*time.Hour)) {
		t.Fatal("Expired boundary wrong")
	}
}

func TestDefaultRetryPolicy(t *testing.T) {
	p := DefaultRetryPolicy()
	if p.Initial <= 0 || p.Max < p.Initial || p.MaxAge < p.Max {
		t.Fatalf("inconsistent defaults: %+v", p)
	}
}
