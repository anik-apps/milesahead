package db

import (
	"database/sql"
	"errors"
	"time"
)

// ModelHealth is one model's record within one model-health step. Zero
// times mean never.
type ModelHealth struct {
	Step, Model     string
	LastOK          time.Time
	LastFail        time.Time
	FailingSince    time.Time // first failure after the last ok
	CallFailStreak  int
	CheckFailStreak int
	LastOutcome     string
	LastError       string
	LastCheck       time.Time
	LastAnswered    time.Time // last real call this model answered
}

// ModelAlert is the state the admin was last emailed about for one alert key.
type ModelAlert struct {
	Key, State string
	Since      time.Time
	NotifiedAt time.Time
}

func modelTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(sqliteTime)
}

func parseModelTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(sqliteTime, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

const modelHealthCols = `step, model, last_ok_at, last_fail_at, failing_since, call_fail_streak,
	check_fail_streak, last_outcome, last_error, last_check_at, last_answered_at`

type modelHealthScanner interface{ Scan(dest ...any) error }

func scanModelHealth(r modelHealthScanner) (ModelHealth, error) {
	var h ModelHealth
	var ok, fail, since, check, answered string
	err := r.Scan(&h.Step, &h.Model, &ok, &fail, &since, &h.CallFailStreak, &h.CheckFailStreak,
		&h.LastOutcome, &h.LastError, &check, &answered)
	h.LastOK, h.LastFail, h.FailingSince = parseModelTime(ok), parseModelTime(fail), parseModelTime(since)
	h.LastCheck, h.LastAnswered = parseModelTime(check), parseModelTime(answered)
	return h, err
}

// GetModelHealth returns the row for (step, model). A missing row is
// returned with found false and Step and Model filled in.
func (d *Database) GetModelHealth(step, model string) (ModelHealth, bool, error) {
	h, err := scanModelHealth(d.db.QueryRow(
		`SELECT `+modelHealthCols+` FROM model_health WHERE step = ? AND model = ?`, step, model))
	if errors.Is(err, sql.ErrNoRows) {
		return ModelHealth{Step: step, Model: model}, false, nil
	}
	if err != nil {
		return ModelHealth{}, false, err
	}
	return h, true, nil
}

// UpsertModelHealth writes h, replacing the row for (h.Step, h.Model).
func (d *Database) UpsertModelHealth(h ModelHealth) error {
	_, err := d.db.Exec(`INSERT INTO model_health (`+modelHealthCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(step, model) DO UPDATE SET
			last_ok_at = excluded.last_ok_at, last_fail_at = excluded.last_fail_at,
			failing_since = excluded.failing_since, call_fail_streak = excluded.call_fail_streak,
			check_fail_streak = excluded.check_fail_streak, last_outcome = excluded.last_outcome,
			last_error = excluded.last_error, last_check_at = excluded.last_check_at,
			last_answered_at = excluded.last_answered_at`,
		h.Step, h.Model, modelTime(h.LastOK), modelTime(h.LastFail), modelTime(h.FailingSince),
		h.CallFailStreak, h.CheckFailStreak, h.LastOutcome, h.LastError,
		modelTime(h.LastCheck), modelTime(h.LastAnswered))
	return err
}

// ListModelHealth returns every row, by step then model.
func (d *Database) ListModelHealth() ([]ModelHealth, error) {
	rows, err := d.db.Query(`SELECT ` + modelHealthCols + ` FROM model_health ORDER BY step, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelHealth
	for rows.Next() {
		h, err := scanModelHealth(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ListModelAlerts returns every stored alert state by key.
func (d *Database) ListModelAlerts() (map[string]ModelAlert, error) {
	rows, err := d.db.Query(`SELECT alert_key, state, since, notified_at FROM model_alerts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ModelAlert{}
	for rows.Next() {
		var a ModelAlert
		var since, notified string
		if err := rows.Scan(&a.Key, &a.State, &since, &notified); err != nil {
			return nil, err
		}
		a.Since, a.NotifiedAt = parseModelTime(since), parseModelTime(notified)
		out[a.Key] = a
	}
	return out, rows.Err()
}

// SaveModelAlerts upserts every alert state in one transaction.
func (d *Database) SaveModelAlerts(alerts []ModelAlert) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, a := range alerts {
		if _, err := tx.Exec(`INSERT INTO model_alerts (alert_key, state, since, notified_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(alert_key) DO UPDATE SET state = excluded.state, since = excluded.since,
			notified_at = excluded.notified_at`,
			a.Key, a.State, modelTime(a.Since), modelTime(a.NotifiedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecordModelAlertSend stores the last model-alert email attempt; sendErr
// is "" when it went out.
func (d *Database) RecordModelAlertSend(at time.Time, sendErr string) error {
	_, err := d.db.Exec(`INSERT INTO model_alert_sends (id, sent_at, error) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET sent_at = excluded.sent_at, error = excluded.error`,
		modelTime(at), sendErr)
	return err
}

// LastModelAlertSend returns the last attempt; a zero time when none.
func (d *Database) LastModelAlertSend() (time.Time, string, error) {
	var at, msg string
	err := d.db.QueryRow(`SELECT sent_at, error FROM model_alert_sends WHERE id = 1`).Scan(&at, &msg)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, "", nil
	}
	return parseModelTime(at), msg, err
}

// LatestArticleCreatedAt is when the newest generated article of any status
// was created; zero when there are none.
func (d *Database) LatestArticleCreatedAt() (time.Time, error) {
	var t time.Time
	err := d.db.QueryRow(`SELECT created_at FROM generated_articles ORDER BY created_at DESC LIMIT 1`).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	return t, err
}

// CountArticlesCreatedSince counts generated articles of any status created at
// or after t.
func (d *Database) CountArticlesCreatedSince(t time.Time) (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM generated_articles WHERE created_at >= ?`,
		t.UTC().Format(sqliteTime)).Scan(&n)
	return n, err
}

// CountSourcesWithRawSince counts sources that added a raw article at or after t.
func (d *Database) CountSourcesWithRawSince(t time.Time) (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(DISTINCT source_id) FROM raw_articles WHERE created_at >= ?`,
		t.UTC().Format(sqliteTime)).Scan(&n)
	return n, err
}
