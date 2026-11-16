package queue

import "time"

// RetryPolicy decides when a failed entry is retried and when it expires.
type RetryPolicy struct {
	// Initial is the delay after the first failed attempt.
	Initial time.Duration
	// Max caps the delay between attempts.
	Max time.Duration
	// MaxAge is how long an entry may stay queued before it expires and a
	// DSN must be generated.
	MaxAge time.Duration
}

// DefaultRetryPolicy mirrors common MTA defaults: retry after 5 minutes,
// doubling up to 4 hours, giving up after 5 days.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		Initial: 5 * time.Minute,
		Max:     4 * time.Hour,
		MaxAge:  5 * 24 * time.Hour,
	}
}

// Backoff returns the delay to wait after the given number of failed
// attempts (attempts >= 1): Initial, 2*Initial, 4*Initial, ... capped at Max.
func (p RetryPolicy) Backoff(attempts int) time.Duration {
	d := p.Initial
	for i := 1; i < attempts; i++ {
		if d >= p.Max {
			break
		}
		d *= 2
	}
	if d > p.Max {
		d = p.Max
	}
	return d
}

// Expired reports whether e has been queued for at least MaxAge at now.
func (p RetryPolicy) Expired(e Entry, now time.Time) bool {
	return now.Sub(e.ReceivedAt) >= p.MaxAge
}

// Next computes the time of the next attempt after a temporary failure of e
// observed at now. e.Attempts must hold the failures before this one. It
// returns ok=false when the entry has expired (or the next attempt would
// fall past its expiry), meaning the caller should Fail it and send a DSN.
func (p RetryPolicy) Next(e Entry, now time.Time) (next time.Time, ok bool) {
	if p.Expired(e, now) {
		return time.Time{}, false
	}
	next = now.Add(p.Backoff(e.Attempts + 1))
	if next.Sub(e.ReceivedAt) >= p.MaxAge {
		return time.Time{}, false
	}
	return next, true
}
