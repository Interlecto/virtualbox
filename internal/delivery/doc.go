// Package delivery executes queued work: the Dispatcher claims queue entries
// and runs their policy actions, and the Client delivers mail over SMTP to
// the recipient domain's MX hosts.
//
// Failures are classified as temporary (rescheduled with queue.RetryPolicy)
// or permanent (removed and bounced with a DSN to the original sender, never
// to a null sender). DKIM signing and SRS (roadmap 1.5) and MTA-STS/DANE
// (6.2) plug in here later.
package delivery
