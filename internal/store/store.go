// Package store keeps synthetic delivery logs in SQLite and answers the
// aggregate queries the diagnostic tools need.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers as "sqlite"
)

// TimeLayout is how timestamps are stored. Fixed-width UTC RFC 3339 strings
// sort lexicographically in time order, so range filters are plain string
// comparisons and hourly buckets are a substring.
const TimeLayout = "2006-01-02T15:04:05Z"

// Message is one delivery-log record.
type Message struct {
	ID              string  `json:"message_id"`
	Timestamp       string  `json:"timestamp"`
	Channel         string  `json:"channel"`  // sms | email
	Provider        string  `json:"provider"` // twilio | telnyx | smtp
	OrgID           string  `json:"org_id"`
	From            string  `json:"from"`
	To              string  `json:"to"`
	Country         string  `json:"country"`
	RecipientDomain string  `json:"recipient_domain,omitempty"` // email only
	Template        string  `json:"template"`
	Status          string  `json:"status"`
	ErrorCode       string  `json:"error_code,omitempty"`
	ErrorMessage    string  `json:"error_message,omitempty"` // bounce reason for email
	Price           float64 `json:"price"`
	Attempts        int     `json:"attempts"`
}

// Incident is a planted problem and its known root cause, used by the eval.
type Incident struct {
	ID          string
	Category    string
	Question    string
	Description string
}

// ErrNotFound is returned when a message id does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS messages (
	message_id       TEXT PRIMARY KEY,
	ts               TEXT NOT NULL,
	channel          TEXT NOT NULL,
	provider         TEXT NOT NULL,
	org_id           TEXT NOT NULL,
	sender           TEXT NOT NULL,
	recipient        TEXT NOT NULL,
	country          TEXT NOT NULL,
	recipient_domain TEXT NOT NULL DEFAULT '',
	template         TEXT NOT NULL,
	status           TEXT NOT NULL,
	error_code       TEXT NOT NULL DEFAULT '',
	error_message    TEXT NOT NULL DEFAULT '',
	price            REAL NOT NULL,
	attempts         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_ts ON messages(ts);
CREATE INDEX IF NOT EXISTS idx_messages_org ON messages(org_id, ts);

CREATE TABLE IF NOT EXISTS incidents (
	id          TEXT PRIMARY KEY,
	category    TEXT NOT NULL,
	question    TEXT NOT NULL,
	description TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// Open opens (or creates) the database at path and ensures the schema exists.
// Use ":memory:" in tests.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single connection keeps ":memory:" databases alive across queries
	// and avoids SQLite write contention. This CLI never needs more.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Reset deletes all rows so the seeder can start from a clean slate.
func (s *Store) Reset(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM messages; DELETE FROM incidents; DELETE FROM meta;`)
	return err
}

// InsertMessages writes messages in one transaction.
func (s *Store) InsertMessages(ctx context.Context, msgs []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO messages
		(message_id, ts, channel, provider, org_id, sender, recipient, country,
		 recipient_domain, template, status, error_code, error_message, price, attempts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range msgs {
		if _, err := stmt.ExecContext(ctx, m.ID, m.Timestamp, m.Channel, m.Provider, m.OrgID,
			m.From, m.To, m.Country, m.RecipientDomain, m.Template, m.Status,
			m.ErrorCode, m.ErrorMessage, m.Price, m.Attempts); err != nil {
			return fmt.Errorf("insert %s: %w", m.ID, err)
		}
	}
	return tx.Commit()
}

// InsertIncidents stores the ground truth for planted incidents.
func (s *Store) InsertIncidents(ctx context.Context, incs []Incident) error {
	for _, inc := range incs {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO incidents (id, category, question, description) VALUES (?, ?, ?, ?)`,
			inc.ID, inc.Category, inc.Question, inc.Description); err != nil {
			return fmt.Errorf("insert incident %s: %w", inc.ID, err)
		}
	}
	return nil
}

// Incidents returns the planted incidents in a stable order.
func (s *Store) Incidents(ctx context.Context) ([]Incident, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, category, question, description FROM incidents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var inc Incident
		if err := rows.Scan(&inc.ID, &inc.Category, &inc.Question, &inc.Description); err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

// SetDatasetTime records the "current time" the synthetic dataset was built
// around, so questions like "since yesterday" stay meaningful later.
func (s *Store) SetDatasetTime(ctx context.Context, t time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO meta (key, value) VALUES ('dataset_now', ?)`, t.UTC().Format(TimeLayout))
	return err
}

// DatasetTime returns the time recorded by SetDatasetTime.
func (s *Store) DatasetTime(ctx context.Context) (time.Time, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'dataset_now'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, errors.New("database is empty: run `diag seed` first")
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(TimeLayout, v)
}

// GetMessage returns one message by id, or ErrNotFound.
func (s *Store) GetMessage(ctx context.Context, id string) (Message, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE message_id = ?`, id)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	return m, err
}

const messageColumns = `message_id, ts, channel, provider, org_id, sender, recipient, country,
	recipient_domain, template, status, error_code, error_message, price, attempts`

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanMessage(sc scanner) (Message, error) {
	var m Message
	err := sc.Scan(&m.ID, &m.Timestamp, &m.Channel, &m.Provider, &m.OrgID, &m.From, &m.To,
		&m.Country, &m.RecipientDomain, &m.Template, &m.Status, &m.ErrorCode, &m.ErrorMessage,
		&m.Price, &m.Attempts)
	return m, err
}
