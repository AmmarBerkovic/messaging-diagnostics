// Package kb is a small knowledge base of provider error codes.
//
// Summaries are written in our own words after checking each code against
// the provider's official documentation; DocURL points at that page.
package kb

import "strings"

// Entry describes one provider error code.
type Entry struct {
	Provider  string `json:"provider"`
	Code      string `json:"code"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Category  string `json:"category"`  // maps to the eval's root cause categories
	Permanent bool   `json:"permanent"` // true if retrying the same request will not help
	DocURL    string `json:"doc_url"`
}

const (
	twilioDocs = "https://www.twilio.com/docs/api/errors/"
	telnyxMsg  = "https://developers.telnyx.com/docs/messaging/messages/error-codes"
	telnyxAPI  = "https://developers.telnyx.com/docs/development/api-fundamentals/api-errors"
	ianaSMTP   = "https://www.iana.org/assignments/smtp-enhanced-status-codes/smtp-enhanced-status-codes.xhtml"
	rfc5321    = "https://www.rfc-editor.org/rfc/rfc5321#section-4.2.3"
)

var entries = []Entry{
	// Twilio
	{"twilio", "30003", "Unreachable destination handset",
		"The carrier could not reach the handset, typically because it is switched off, out of coverage, roaming, or cannot take SMS. Usually transient for a single number.",
		"unreachable_handset", false, twilioDocs + "30003"},
	{"twilio", "30004", "Message blocked",
		"The destination refused the message. Causes include the recipient having opted out, a landline, local do-not-disturb rules, or carrier content filtering.",
		"carrier_filtering", true, twilioDocs + "30004"},
	{"twilio", "30005", "Unknown destination handset",
		"The number is unknown to the carrier, cannot receive SMS, or was briefly unreachable. Check the number is valid E.164 and still in service.",
		"invalid_number", true, twilioDocs + "30005"},
	{"twilio", "30006", "Landline or unreachable carrier",
		"The recipient is a landline, or the carrier could not be reached from this sender type. Validate line type before sending.",
		"invalid_number", true, twilioDocs + "30006"},
	{"twilio", "30007", "Message filtered",
		"Twilio or the carrier filtered the message as likely spam or a policy violation. A spike from one sender usually points at that sender's content or registration rather than the recipients.",
		"carrier_filtering", true, twilioDocs + "30007"},
	{"twilio", "30008", "Unknown error",
		"The carrier reported a failure without a specific reason. Isolated occurrences are normal background noise; a persistent pattern needs support with sample message ids.",
		"unknown", false, twilioDocs + "30008"},
	{"twilio", "21211", "Invalid 'To' phone number",
		"The destination number is malformed, most often not in E.164 format. Rejected at the API, so nothing was sent.",
		"invalid_number", true, twilioDocs + "21211"},
	{"twilio", "21610", "Attempt to send to unsubscribed recipient",
		"The recipient replied STOP (or a similar keyword) to this sender, so further messages are blocked until they opt back in with START.",
		"opt_out", true, twilioDocs + "21610"},
	{"twilio", "21614", "'To' number is not a valid mobile number",
		"The number is not a mobile number that can receive SMS, for example a landline or malformed number.",
		"invalid_number", true, twilioDocs + "21614"},
	{"twilio", "20003", "Permission denied",
		"The API credentials were rejected: wrong, expired, deleted, or belonging to another account. Every request from that account fails until the credentials are fixed.",
		"auth_failure", true, twilioDocs + "20003"},
	{"twilio", "20429", "Too many requests",
		"The account exceeded Twilio's concurrent API request limit (HTTP 429). Those requests were not processed; retry with exponential backoff and throttle sending.",
		"rate_limited", false, twilioDocs + "20429"},

	// Telnyx
	{"telnyx", "40001", "Not routable",
		"The destination is a landline or otherwise cannot be routed for messaging.",
		"invalid_number", true, telnyxMsg},
	{"telnyx", "40002", "Blocked as spam (temporary)",
		"The carrier's spam filter rejected the message. Review content and sending patterns before retrying.",
		"carrier_filtering", false, telnyxMsg},
	{"telnyx", "40300", "Blocked (STOP)",
		"The recipient opted out by replying STOP, so the message was not sent.",
		"opt_out", true, telnyxMsg},
	{"telnyx", "40310", "Invalid 'to' address",
		"The destination number is invalid. Check the number and its formatting.",
		"invalid_number", true, telnyxMsg},
	{"telnyx", "40011", "Rate limit exceeded (upstream)",
		"The carrier's throughput limit was exceeded. Spread sends out over time.",
		"rate_limited", false, telnyxMsg},
	{"telnyx", "10009", "Authentication failed",
		"The API request could not be authenticated, for example because the API key is invalid, revoked or expired.",
		"auth_failure", true, telnyxAPI},
	{"telnyx", "10011", "Too many requests",
		"The API rate limit was exceeded. Back off and retry, and pace bulk sends.",
		"rate_limited", false, telnyxAPI},

	// SMTP (enhanced status codes from RFC 3463 and later RFCs, via the IANA registry)
	{"smtp", "5.1.1", "Bad destination mailbox address",
		"The receiving server says the mailbox does not exist (usually seen as 550 5.1.1). A hard bounce; remove the address.",
		"invalid_address", true, ianaSMTP},
	{"smtp", "5.1.2", "Bad destination system address",
		"The recipient's domain or mail host cannot be found or does not accept mail. When every address at one domain bounces this way, the domain itself has gone away.",
		"recipient_domain_failure", true, ianaSMTP},
	{"smtp", "5.1.10", "Recipient address has null MX",
		"The domain publishes a null MX record, declaring that it accepts no email (sent with 556).",
		"recipient_domain_failure", true, ianaSMTP},
	{"smtp", "5.2.2", "Mailbox full",
		"The recipient's mailbox is over quota (usually 552). Often temporary from the recipient's side, but repeated failures mean the mailbox is abandoned.",
		"mailbox_full", false, ianaSMTP},
	{"smtp", "5.7.1", "Delivery not authorized, message refused",
		"The receiving server refused the message on policy grounds, such as sender reputation or content.",
		"policy_rejection", true, ianaSMTP},
	{"smtp", "5.7.8", "Authentication credentials invalid",
		"Our own SMTP login to the relay was rejected (usually 535). All mail through that account fails until credentials are rotated.",
		"auth_failure", true, ianaSMTP},
	{"smtp", "421", "Service not available",
		"The server is closing the connection and asks the sender to try later. A temporary condition, often used for throttling.",
		"rate_limited", false, rfc5321},
}

// Lookup finds an entry by provider and code. Provider matching ignores case.
func Lookup(provider, code string) (Entry, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	code = strings.TrimSpace(code)
	for _, e := range entries {
		if e.Provider == provider && e.Code == code {
			return e, true
		}
	}
	return Entry{}, false
}

// All returns a copy of every entry.
func All() []Entry {
	return append([]Entry(nil), entries...)
}

// Root cause categories. The model must pick one when it reports a
// diagnosis, and the eval compares that choice to the planted ground truth.
const (
	CauseCarrierFiltering = "carrier_filtering"
	CauseOptOut           = "opt_out"
	CauseInvalidNumber    = "invalid_number"
	CauseRateLimited      = "rate_limited"
	CauseDomainFailure    = "recipient_domain_failure"
	CauseAuthFailure      = "auth_failure"
)

// RootCauses lists every category, including those used only for
// background noise and the catch-alls.
var RootCauses = []string{
	CauseCarrierFiltering, CauseOptOut, CauseInvalidNumber, CauseRateLimited,
	CauseDomainFailure, CauseAuthFailure,
	"unreachable_handset", "invalid_address", "mailbox_full", "policy_rejection",
	"no_issue", "unknown",
}
