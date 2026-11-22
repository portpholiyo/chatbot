// Package store persists chat history in PostgreSQL.
package store

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Status values stored in messages.status.
const (
	StatusAnswered = "answered"
	StatusRefused  = "refused"
	StatusError    = "error"
)

// Message is one question/answer exchange.
type Message struct {
	ID           int64     `json:"id"`
	SessionID    string    `json:"session_id"`
	CreatedAt    time.Time `json:"created_at"`
	IPHash       string    `json:"ip_hash"`
	UserAgent    string    `json:"user_agent"`
	Question     string    `json:"question"`
	Answer       string    `json:"answer"`
	Status       string    `json:"status"`
	KnowledgeGap bool      `json:"knowledge_gap"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	LatencyMS    int       `json:"latency_ms"`
	Error        string    `json:"error,omitempty"`
}

// Turn is a past question and its answer, used as conversation context.
type Turn struct {
	Question string
	Answer   string
}

// ListFilter narrows the admin listing.
type ListFilter struct {
	Status  string // empty = any
	GapOnly bool
	Before  int64 // return rows with id < Before; 0 = newest
	Limit   int
}

// Store wraps a pgx connection pool.
type Store struct{ pool *pgxpool.Pool }

// Open connects (retrying while the database starts), then applies migrations.
func Open(ctx context.Context, host, port, db, user, password string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(fmt.Sprintf("host=%s port=%s dbname=%s user=%s sslmode=disable", host, port, db, user))
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.Password = password
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour

	var pool *pgxpool.Pool
	deadline := time.Now().Add(60 * time.Second)
	for {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				break
			}
			pool.Close()
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fmt.Errorf("database not reachable: %w", err)
		}
		slog.Warn("waiting for database", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close releases the connection pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const migrationLockID = 727274

func (s *Store) migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return err
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID) }()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		slog.Info("applied migration", "version", name)
	}
	return nil
}

// Insert saves a message and returns its id.
func (s *Store) Insert(ctx context.Context, m *Message) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO messages (session_id, ip_hash, user_agent, question, answer, status,
			knowledge_gap, model, input_tokens, output_tokens, latency_ms, error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		m.SessionID, m.IPHash, m.UserAgent, m.Question, m.Answer, m.Status,
		m.KnowledgeGap, m.Model, m.InputTokens, m.OutputTokens, m.LatencyMS, m.Error).Scan(&id)
	return id, err
}

// History returns the most recent answered turns of a session, oldest first.
func (s *Store) History(ctx context.Context, sessionID string, limit int) ([]Turn, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT question, answer FROM messages
		WHERE session_id = $1 AND status = 'answered'
		ORDER BY id DESC LIMIT $2`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []Turn
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.Question, &t.Answer); err != nil {
			return nil, err
		}
		turns = append(turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
		turns[i], turns[j] = turns[j], turns[i]
	}
	return turns, nil
}

// CountToday returns how many exchanges were recorded since 00:00 UTC.
func (s *Store) CountToday(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM messages
		WHERE created_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`).Scan(&n)
	return n, err
}

// List returns messages newest first, for the admin endpoint.
func (s *Store) List(ctx context.Context, f ListFilter) ([]Message, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, session_id::text, created_at, ip_hash, user_agent, question, answer, status,
			knowledge_gap, model, input_tokens, output_tokens, latency_ms, error
		FROM messages
		WHERE ($1 = '' OR status = $1)
		  AND (NOT $2 OR knowledge_gap)
		  AND ($3 = 0 OR id < $3)
		ORDER BY id DESC LIMIT $4`, f.Status, f.GapOnly, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Message, 0, f.Limit)
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.SessionID, &m.CreatedAt, &m.IPHash, &m.UserAgent, &m.Question,
			&m.Answer, &m.Status, &m.KnowledgeGap, &m.Model, &m.InputTokens, &m.OutputTokens,
			&m.LatencyMS, &m.Error); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
