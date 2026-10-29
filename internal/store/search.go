package store

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// failedStatuses are the terminal states that count as a delivery failure.
const failedSQL = `status IN ('undelivered', 'failed', 'bounced')`

// Filter narrows a search. Empty fields and zero times mean "no filter".
type Filter struct {
	Channel         string
	Provider        string
	Country         string
	OrgID           string
	Sender          string
	Status          string
	ErrorCode       string
	RecipientDomain string
	Since           time.Time // inclusive
	Until           time.Time // exclusive
}

// Summary is what a search returns: aggregates plus a handful of samples,
// never the full set of matching rows.
type Summary struct {
	Total         int          `json:"total"`
	Failed        int          `json:"failed"`
	FailureRate   float64      `json:"failure_rate_pct"`
	FirstSeen     string       `json:"first_seen,omitempty"`
	LastSeen      string       `json:"last_seen,omitempty"`
	ByStatus      []Bucket     `json:"by_status"`
	TopErrorCodes []ErrorCount `json:"top_error_codes"`
	BySender      []Bucket     `json:"by_sender"`
	ByCountry     []Bucket     `json:"by_country"`
	ByOrg         []Bucket     `json:"by_org"`
	ByDomain      []Bucket     `json:"by_recipient_domain,omitempty"`
	Hourly        []Bucket     `json:"hourly"`
	Samples       []Message    `json:"sample_messages"`
}

// Bucket is one row of a breakdown.
type Bucket struct {
	Key         string  `json:"key"`
	Total       int     `json:"total"`
	Failed      int     `json:"failed"`
	FailureRate float64 `json:"failure_rate_pct"`
}

// ErrorCount summarises one error code within the search.
type ErrorCount struct {
	Provider  string `json:"provider"`
	Code      string `json:"error_code"`
	Message   string `json:"example_message"`
	Count     int    `json:"count"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

const (
	breakdownLimit = 5
	errorCodeLimit = 8
	sampleLimit    = 5
)

// where builds the SQL WHERE clause and its arguments for a filter.
// Only values are parameterised; column names are fixed strings.
func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(col, val string) {
		if val != "" {
			conds = append(conds, col+" = ?")
			args = append(args, val)
		}
	}
	add("channel", f.Channel)
	add("provider", f.Provider)
	add("country", f.Country)
	add("org_id", f.OrgID)
	add("sender", f.Sender)
	add("status", f.Status)
	add("error_code", f.ErrorCode)
	add("recipient_domain", f.RecipientDomain)
	if !f.Since.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, f.Since.UTC().Format(TimeLayout))
	}
	if !f.Until.IsZero() {
		conds = append(conds, "ts < ?")
		args = append(args, f.Until.UTC().Format(TimeLayout))
	}
	if len(conds) == 0 {
		return "1 = 1", nil
	}
	return strings.Join(conds, " AND "), args
}

// Search aggregates the messages matching f.
func (s *Store) Search(ctx context.Context, f Filter) (Summary, error) {
	where, args := f.where()
	var sum Summary

	var first, last *string
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN `+failedSQL+` THEN 1 ELSE 0 END), 0), MIN(ts), MAX(ts)
		 FROM messages WHERE `+where, args...).Scan(&sum.Total, &sum.Failed, &first, &last)
	if err != nil {
		return Summary{}, fmt.Errorf("count: %w", err)
	}
	sum.FailureRate = rate(sum.Failed, sum.Total)
	if first != nil {
		sum.FirstSeen, sum.LastSeen = *first, *last
	}
	if sum.Total == 0 {
		return sum, nil
	}

	breakdowns := []struct {
		dest  *[]Bucket
		expr  string
		limit int
	}{
		{&sum.ByStatus, "status", 10},
		{&sum.BySender, "sender", breakdownLimit},
		{&sum.ByCountry, "country", breakdownLimit},
		{&sum.ByOrg, "org_id", breakdownLimit},
		{&sum.ByDomain, "recipient_domain", breakdownLimit},
		// The first 13 characters of a timestamp are "YYYY-MM-DDTHH".
		{&sum.Hourly, "substr(ts, 1, 13)", 0},
	}
	for _, b := range breakdowns {
		if *b.dest, err = s.breakdown(ctx, b.expr, where, args, b.limit); err != nil {
			return Summary{}, err
		}
	}

	if sum.TopErrorCodes, err = s.topErrorCodes(ctx, where, args); err != nil {
		return Summary{}, err
	}
	if sum.Samples, err = s.samples(ctx, where, args, sum.Failed > 0); err != nil {
		return Summary{}, err
	}
	return sum, nil
}

// breakdown groups matching rows by expr. A limit of 0 means "all groups,
// in key order" (used for the hourly series); otherwise the groups with the
// most failures come first.
func (s *Store) breakdown(ctx context.Context, expr, where string, args []any, limit int) ([]Bucket, error) {
	q := `SELECT ` + expr + ` AS k, COUNT(*), SUM(CASE WHEN ` + failedSQL + ` THEN 1 ELSE 0 END)
		FROM messages WHERE ` + where + ` AND ` + expr + ` != '' GROUP BY k`
	if limit > 0 {
		q += fmt.Sprintf(` ORDER BY 3 DESC, 2 DESC, k LIMIT %d`, limit)
	} else {
		q += ` ORDER BY k`
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("breakdown by %s: %w", expr, err)
	}
	defer rows.Close()

	var out []Bucket
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.Key, &b.Total, &b.Failed); err != nil {
			return nil, err
		}
		b.FailureRate = rate(b.Failed, b.Total)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) topErrorCodes(ctx context.Context, where string, args []any) ([]ErrorCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, error_code, MAX(error_message), COUNT(*), MIN(ts), MAX(ts)
		 FROM messages WHERE `+where+` AND error_code != ''
		 GROUP BY provider, error_code ORDER BY 4 DESC, 2 LIMIT ?`,
		withArg(args, errorCodeLimit)...)
	if err != nil {
		return nil, fmt.Errorf("top error codes: %w", err)
	}
	defer rows.Close()

	var out []ErrorCount
	for rows.Next() {
		var e ErrorCount
		if err := rows.Scan(&e.Provider, &e.Code, &e.Message, &e.Count, &e.FirstSeen, &e.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// samples returns the most recent matching rows, preferring failures so the
// model sees concrete evidence for the problem it is diagnosing.
func (s *Store) samples(ctx context.Context, where string, args []any, failedOnly bool) ([]Message, error) {
	if failedOnly {
		where += " AND " + failedSQL
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+messageColumns+` FROM messages WHERE `+where+` ORDER BY ts DESC LIMIT ?`,
		withArg(args, sampleLimit)...)
	if err != nil {
		return nil, fmt.Errorf("samples: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// rate returns failed/total as a percentage rounded to one decimal place.
func rate(failed, total int) float64 {
	if total == 0 {
		return 0
	}
	return math.Round(float64(failed)*1000/float64(total)) / 10
}

// withArg returns a copy of args with v appended, so callers never share a
// backing array with the slice built by Filter.where.
func withArg(args []any, v any) []any {
	out := make([]any, 0, len(args)+1)
	return append(append(out, args...), v)
}
