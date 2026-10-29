// Package seed generates SYNTHETIC delivery logs: mostly normal traffic with
// a set of planted incidents whose root causes are known in advance.
//
// Nothing here is real data. Phone numbers come from ranges reserved for
// fiction (UK 07700 900xxx, North American 555-01xx), email domains use the
// reserved .example TLD, and organisations are invented.
package seed

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/ammarberkovic/messaging-diagnostics/internal/store"
)

// Config controls generation. The same Config always yields the same data.
type Config struct {
	Seed     uint64
	Now      time.Time // the dataset's "current time"; traffic covers the hours before it
	Hours    int       // length of the window, default 72
	Baseline int       // number of normal messages, default 3000
}

type org struct {
	id        string
	provider  string            // SMS provider
	senders   map[string]string // country -> sender number
	countries []string
	emailFrom string
	emailMix  float64 // share of traffic that is email
	templates []string
}

var orgs = []org{
	{"org_acme", "telnyx", map[string]string{"US": "+14155550101", "CA": "+14165550101"}, []string{"US", "US", "CA"}, "alerts@acme.example", 0.2, []string{"otp_login", "order_shipped", "flash_sale"}},
	{"org_brightpath", "twilio", map[string]string{"GB": "+447700900500", "US": "+12125550105"}, []string{"GB", "GB", "US"}, "hello@brightpath.example", 0.2, []string{"appointment_reminder", "promo_weekly"}},
	{"org_cedar", "twilio", map[string]string{"US": "+13125550110"}, []string{"US"}, "team@cedar.example", 0.25, []string{"delivery_update", "survey"}},
	{"org_driftwood", "telnyx", map[string]string{"US": "+16175550120"}, []string{"US"}, "news@driftwood.example", 0.2, []string{"spring_promo", "otp_login"}},
	{"org_evergreen", "twilio", map[string]string{"GB": "+447700900600"}, []string{"GB"}, "billing@evergreen.example", 0.8, []string{"invoice_ready", "password_reset"}},
	{"org_foxglove", "twilio", map[string]string{"US": "+17185550130", "GB": "+447700900700"}, []string{"US", "GB"}, "support@foxglove.example", 0.15, []string{"otp_login", "booking_confirmed"}},
}

var emailDomains = []string{"inbox.example", "mailbox.example", "postbox.example", "corpmail.example"}

const deadDomain = "oldmail.example"

// Fixed values used by the planted incidents.
const (
	filteredSender = "+447700900500" // org_brightpath's GB sender
)

type generator struct {
	rng  *rand.Rand
	cfg  Config
	ids  map[string]bool
	msgs []store.Message
}

// Generate returns the synthetic messages and the ground truth for the
// incidents planted in them.
func Generate(cfg Config) ([]store.Message, []store.Incident) {
	if cfg.Hours == 0 {
		cfg.Hours = 72
	}
	if cfg.Baseline == 0 {
		cfg.Baseline = 3000
	}
	cfg.Now = cfg.Now.UTC().Truncate(time.Minute)
	g := &generator{
		rng: rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9e3779b97f4a7c15)),
		cfg: cfg,
		ids: map[string]bool{},
	}

	start := cfg.Now.Add(-time.Duration(cfg.Hours) * time.Hour)
	for range cfg.Baseline {
		o := orgs[g.rng.IntN(len(orgs))]
		g.msgs = append(g.msgs, g.normal(o, g.timeBetween(start, cfg.Now)))
	}

	incidents := []store.Incident{
		g.carrierFiltering(),
		g.optOut(start),
		g.invalidNumbers(),
		g.rateLimitBurst(),
		g.deadDomain(),
		g.expiredCredentials(),
	}
	return g.msgs, incidents
}

// normal builds one ordinary message with realistic background noise.
func (g *generator) normal(o org, ts time.Time) store.Message {
	m := g.base(o, ts)
	if g.rng.Float64() < o.emailMix {
		domain := pick(g.rng, emailDomains)
		// Only org_evergreen has customers on the domain that later dies.
		if o.id == "org_evergreen" && g.rng.Float64() < 0.35 {
			domain = deadDomain
		}
		g.fillEmail(&m, o, domain)
	} else {
		g.fillSMS(&m, o, pick(g.rng, o.countries))
	}
	return m
}

// base returns the fields every message has, before channel-specific ones.
func (g *generator) base(o org, ts time.Time) store.Message {
	return store.Message{
		ID:        g.newID(),
		Timestamp: ts.Format(store.TimeLayout),
		OrgID:     o.id,
		Template:  pick(g.rng, o.templates),
		Attempts:  1,
	}
}

func (g *generator) fillSMS(m *store.Message, o org, country string) {
	m.Channel = "sms"
	m.Provider = o.provider
	m.Country = country
	m.From = o.senders[country]
	m.To = g.phone(country)
	m.Price = 0.0079
	if country == "GB" {
		m.Price = 0.0400
	}

	r := g.rng.Float64()
	switch {
	case r < 0.93:
		m.Status = "delivered"
	case r < 0.96:
		m.Status = "sent" // accepted by the carrier, no receipt yet
	default:
		m.Status = "undelivered"
		m.Attempts = 1 + g.rng.IntN(3)
		if o.provider == "twilio" {
			code := pick(g.rng, []string{"30003", "30005", "30008"})
			m.ErrorCode, m.ErrorMessage = code, smsErrorText[code]
		} else {
			m.ErrorCode, m.ErrorMessage = "40001", smsErrorText["40001"]
		}
	}
}

func (g *generator) fillEmail(m *store.Message, o org, domain string) {
	m.Channel = "email"
	m.Provider = "smtp"
	m.Country = pick(g.rng, o.countries)
	m.From = o.emailFrom
	m.RecipientDomain = domain
	m.To = fmt.Sprintf("user%04d@%s", g.rng.IntN(10000), domain)
	m.Price = 0.0001

	r := g.rng.Float64()
	switch {
	case r < 0.96:
		m.Status = "delivered"
	case r < 0.98:
		m.Status = "sent"
	default:
		m.Status = "bounced"
		code := pick(g.rng, []string{"5.1.1", "5.2.2"})
		m.ErrorCode, m.ErrorMessage = code, emailErrorText[code]
	}
}

var smsErrorText = map[string]string{
	"30003": "Unreachable destination handset",
	"30005": "Unknown destination handset",
	"30007": "Message filtered",
	"30008": "Unknown error",
	"21610": "Attempt to send to unsubscribed recipient",
	"20003": "Permission denied: authentication failed",
	"40001": "Not routable",
	"40310": "Invalid 'to' address",
	"10011": "Too many requests",
}

var emailErrorText = map[string]string{
	"5.1.1": "550 5.1.1 The email account that you tried to reach does not exist",
	"5.2.2": "552 5.2.2 Mailbox full, over quota",
	"5.1.2": "550 5.1.2 Host unknown: domain " + deadDomain + " not found",
}

// newID returns a unique id such as "msg_8f2a91".
func (g *generator) newID() string {
	for {
		id := fmt.Sprintf("msg_%06x", g.rng.Uint32()&0xffffff)
		if !g.ids[id] {
			g.ids[id] = true
			return id
		}
	}
}

// phone returns a fictional number for the country.
func (g *generator) phone(country string) string {
	switch country {
	case "GB":
		return fmt.Sprintf("+447700900%03d", g.rng.IntN(1000)) // Ofcom drama range
	case "CA":
		return fmt.Sprintf("+1%s55501%02d", pick(g.rng, []string{"416", "604", "514"}), g.rng.IntN(100))
	default:
		return fmt.Sprintf("+1%s55501%02d", pick(g.rng, []string{"212", "415", "312", "617", "718"}), g.rng.IntN(100))
	}
}

func (g *generator) timeBetween(from, to time.Time) time.Time {
	span := to.Sub(from)
	return from.Add(time.Duration(g.rng.Int64N(int64(span)))).Truncate(time.Second)
}

func pick[T any](rng *rand.Rand, xs []T) T {
	return xs[rng.IntN(len(xs))]
}

// Load replaces the contents of st with freshly generated data and returns
// how many messages were written.
func Load(ctx context.Context, st *store.Store, cfg Config) (int, error) {
	msgs, incidents := Generate(cfg)
	if err := st.Reset(ctx); err != nil {
		return 0, fmt.Errorf("reset: %w", err)
	}
	if err := st.InsertMessages(ctx, msgs); err != nil {
		return 0, err
	}
	if err := st.InsertIncidents(ctx, incidents); err != nil {
		return 0, err
	}
	if err := st.SetDatasetTime(ctx, cfg.Now.UTC().Truncate(time.Minute)); err != nil {
		return 0, err
	}
	return len(msgs), nil
}
