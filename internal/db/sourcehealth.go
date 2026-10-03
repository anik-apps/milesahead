package db

import (
	"fmt"
	"time"
)

// SourceHealth is what the scanner has collected from one source: when it
// last stored an article and how many it stored recently. A feed that has
// stopped working shows up as an old LastArticleAt.
type SourceHealth struct {
	SourceID int64
	// LastArticleAt is when the newest article from the source was stored;
	// zero if none ever was.
	LastArticleAt time.Time
	// Recent counts articles stored since the cutoff passed to
	// ListSourceHealth.
	Recent int
	// Total counts every article ever stored from the source.
	Total int
}

// Stale reports whether an active source has stored nothing for longer than
// after (or ever): its feed has probably moved or broken.
func (h SourceHealth) Stale(active bool, now time.Time, after time.Duration) bool {
	return active && (h.LastArticleAt.IsZero() || now.Sub(h.LastArticleAt) > after)
}

// sqliteTime is the layout CURRENT_TIMESTAMP writes.
const sqliteTime = "2006-01-02 15:04:05"

// ListSourceHealth returns each source's health, keyed by source id, with
// Recent counting articles stored at or after since.
func (d *Database) ListSourceHealth(since time.Time) (map[int64]SourceHealth, error) {
	rows, err := d.db.Query(`
		SELECT s.id, COALESCE(MAX(r.created_at), ''),
		       COALESCE(SUM(CASE WHEN r.created_at >= ? THEN 1 ELSE 0 END), 0),
		       COUNT(r.id)
		FROM sources s LEFT JOIN raw_articles r ON r.source_id = s.id
		GROUP BY s.id`, since.UTC().Format(sqliteTime))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]SourceHealth{}
	for rows.Next() {
		var h SourceHealth
		var last interface{}
		if err := rows.Scan(&h.SourceID, &last, &h.Recent, &h.Total); err != nil {
			return nil, err
		}
		h.LastArticleAt, err = parseDBTime(last)
		if err != nil {
			return nil, fmt.Errorf("source %d: %w", h.SourceID, err)
		}
		out[h.SourceID] = h
	}
	return out, rows.Err()
}

// parseDBTime reads a timestamp an aggregate returned: the driver gives it
// back as text (MAX loses the column's type) or, sometimes, as a time.
func parseDBTime(v interface{}) (time.Time, error) {
	switch t := v.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return t, nil
	case []byte:
		return parseDBTime(string(t))
	case string:
		if t == "" {
			return time.Time{}, nil
		}
		for _, layout := range []string{sqliteTime, time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999 -0700 MST"} {
			if p, err := time.Parse(layout, t); err == nil {
				return p, nil
			}
		}
		return time.Time{}, fmt.Errorf("unrecognised time %q", t)
	}
	return time.Time{}, fmt.Errorf("unexpected time type %T", v)
}

// CountArticlesByStatus returns how many articles are in each status.
func (d *Database) CountArticlesByStatus() (map[string]int, error) {
	rows, err := d.db.Query(`SELECT status, COUNT(*) FROM generated_articles GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}
