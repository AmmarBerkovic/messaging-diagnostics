package seed

import (
	"time"

	"github.com/ammarberkovic/messaging-diagnostics/internal/kb"
	"github.com/ammarberkovic/messaging-diagnostics/internal/store"
)

// Each method plants one incident into g.msgs and returns its ground truth.
// Incidents either add extra rows, rewrite baseline rows that fall inside the
// incident, or both.

// carrierFiltering: from 14:00 UTC yesterday, Twilio filters most UK SMS
// sent from one org_brightpath sender (30007). Its other senders are fine.
func (g *generator) carrierFiltering() store.Incident {
	now := g.cfg.Now
	start := time.Date(now.Year(), now.Month(), now.Day()-1, 14, 0, 0, 0, time.UTC)
	o := orgByID("org_brightpath")

	// Extra campaign traffic from the affected sender during the incident.
	for range int(now.Sub(start).Hours() * 12) {
		m := g.base(o, g.timeBetween(start, now))
		g.fillSMS(&m, o, "GB")
		m.Template = "promo_weekly"
		g.msgs = append(g.msgs, m)
	}
	for i := range g.msgs {
		m := &g.msgs[i]
		if m.From == filteredSender && after(m, start) && g.rng.Float64() < 0.75 {
			setFailure(m, "undelivered", "30007", smsErrorText["30007"])
		}
	}
	return store.Incident{
		ID:          "inc-1-carrier-filtering",
		Category:    kb.CauseCarrierFiltering,
		Question:    "Why are UK SMS failing since yesterday?",
		Description: "Twilio 30007 carrier filtering on UK SMS from org_brightpath sender " + filteredSender + " starting " + start.Format(store.TimeLayout),
	}
}

// optOut: six org_cedar recipients replied STOP but keep being messaged (21610).
func (g *generator) optOut(windowStart time.Time) store.Incident {
	o := orgByID("org_cedar")
	numbers := []string{"+13125550111", "+13125550123", "+12125550134", "+14155550145", "+16175550156", "+17185550167"}
	for _, n := range numbers {
		for range 5 {
			m := g.base(o, g.timeBetween(windowStart, g.cfg.Now))
			g.fillSMS(&m, o, "US")
			m.To = n
			setFailure(&m, "failed", "21610", smsErrorText["21610"])
			g.msgs = append(g.msgs, m)
		}
	}
	return store.Incident{
		ID:          "inc-2-opt-out",
		Category:    kb.CauseOptOut,
		Question:    "Messages from org_cedar to a few specific recipients keep failing. Why?",
		Description: "Six org_cedar recipients opted out (Twilio 21610) and are still being sent to",
	}
}

// invalidNumbers: org_driftwood uploads a bad contact list and sends a
// campaign via Telnyx 30 hours ago; many numbers are invalid or landlines.
func (g *generator) invalidNumbers() store.Incident {
	o := orgByID("org_driftwood")
	start := g.cfg.Now.Add(-30 * time.Hour)
	for range 300 {
		m := g.base(o, g.timeBetween(start, start.Add(time.Hour)))
		g.fillSMS(&m, o, "US")
		m.Template = "spring_promo"
		switch r := g.rng.Float64(); {
		case r < 0.45:
			m.To = m.To[:len(m.To)-4] // truncated number, not valid E.164 length
			setFailure(&m, "failed", "40310", smsErrorText["40310"])
		case r < 0.55:
			setFailure(&m, "undelivered", "40001", smsErrorText["40001"])
		}
		g.msgs = append(g.msgs, m)
	}
	return store.Incident{
		ID:          "inc-3-invalid-numbers",
		Category:    kb.CauseInvalidNumber,
		Question:    "org_driftwood sent a campaign to a newly uploaded contact list and lots of texts failed. What went wrong?",
		Description: "org_driftwood spring_promo campaign to a bad list: Telnyx 40310 invalid numbers and 40001 landlines",
	}
}

// rateLimitBurst: org_acme fires a flash sale 8 hours ago, far faster than
// its Telnyx API limit, and many requests are rejected with 10011.
func (g *generator) rateLimitBurst() store.Incident {
	o := orgByID("org_acme")
	start := g.cfg.Now.Add(-8 * time.Hour)
	for range 400 {
		m := g.base(o, g.timeBetween(start, start.Add(20*time.Minute)))
		g.fillSMS(&m, o, "US")
		m.Template = "flash_sale"
		if g.rng.Float64() < 0.4 {
			setFailure(&m, "failed", "10011", smsErrorText["10011"])
		}
		g.msgs = append(g.msgs, m)
	}
	return store.Incident{
		ID:          "inc-4-rate-limit",
		Category:    kb.CauseRateLimited,
		Question:    "A large batch of org_acme messages failed all at once in the last twelve hours. What happened?",
		Description: "org_acme flash_sale burst hit the Telnyx API rate limit (10011) for 20 minutes",
	}
}

// deadDomain: 36 hours ago the domain many org_evergreen customers use
// stopped accepting mail, so every email there hard-bounces (5.1.2).
func (g *generator) deadDomain() store.Incident {
	o := orgByID("org_evergreen")
	start := g.cfg.Now.Add(-36 * time.Hour)
	for range 120 {
		m := g.base(o, g.timeBetween(start, g.cfg.Now))
		g.fillEmail(&m, o, deadDomain)
		g.msgs = append(g.msgs, m)
	}
	for i := range g.msgs {
		m := &g.msgs[i]
		if m.RecipientDomain == deadDomain && after(m, start) {
			setFailure(m, "bounced", "5.1.2", emailErrorText["5.1.2"])
		}
	}
	return store.Incident{
		ID:          "inc-5-dead-domain",
		Category:    kb.CauseDomainFailure,
		Question:    "Why has org_evergreen's email bounce rate jumped?",
		Description: "Recipient domain " + deadDomain + " stopped accepting mail; all org_evergreen email to it hard-bounces 550 5.1.2",
	}
}

// expiredCredentials: org_foxglove's Twilio credentials expire 5 hours ago,
// so every SMS it sends after that is rejected with 20003.
func (g *generator) expiredCredentials() store.Incident {
	start := g.cfg.Now.Add(-5 * time.Hour)
	for i := range g.msgs {
		m := &g.msgs[i]
		if m.OrgID == "org_foxglove" && m.Channel == "sms" && after(m, start) {
			setFailure(m, "failed", "20003", smsErrorText["20003"])
		}
	}
	return store.Incident{
		ID:          "inc-6-auth-expired",
		Category:    kb.CauseAuthFailure,
		Question:    "Every SMS from org_foxglove has failed for the last few hours. Why?",
		Description: "org_foxglove Twilio credentials expired; all SMS rejected with 20003 since " + start.Format(store.TimeLayout),
	}
}

// setFailure marks a message as failed. API-level rejections ("failed")
// are never charged and are not retried.
func setFailure(m *store.Message, status, code, text string) {
	m.Status, m.ErrorCode, m.ErrorMessage = status, code, text
	if status == "failed" {
		m.Price, m.Attempts = 0, 1
	}
}

func after(m *store.Message, t time.Time) bool {
	return m.Timestamp >= t.UTC().Format(store.TimeLayout)
}

func orgByID(id string) org {
	for _, o := range orgs {
		if o.id == id {
			return o
		}
	}
	panic("seed: unknown org " + id)
}
