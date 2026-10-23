package store

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

// CategoryRe is what a category name in a retention rule may look like; it
// is embedded in the TTL expression, so it must stay this strict.
var CategoryRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// RetentionTTL builds the TTL clause for the logs table: a default number of
// days, and per category overrides such as {"wifi": 30, "ips": 365}.
//
// Rows match exactly one conditional rule (their category or the default),
// and an unconditional rule at the longest retention lets ClickHouse drop
// whole parts once everything in them is past every limit.
//
// move, when set, is a TTL rule that moves older parts to cold storage
// (see ColdTTL); it goes first so it is kept next to the delete rules.
func RetentionTTL(days int, perCategory map[string]int, move string) string {
	expr := func(d int) string { return fmt.Sprintf("toDateTime(ts) + INTERVAL %d DAY DELETE", d) }
	prefix := ""
	if move != "" {
		prefix = move + ", "
	}
	cats := slices.Sorted(maps.Keys(perCategory))
	if len(cats) == 0 {
		return prefix + expr(days)
	}
	longest := days
	quoted := make([]string, len(cats))
	rules := []string{}
	for i, c := range cats {
		d := perCategory[c]
		longest = max(longest, d)
		quoted[i] = "'" + c + "'"
		rules = append(rules, fmt.Sprintf("%s WHERE category = '%s'", expr(d), c))
	}
	rules = append(rules, fmt.Sprintf("%s WHERE category NOT IN (%s)", expr(days), strings.Join(quoted, ", ")))
	return prefix + strings.Join(append([]string{expr(longest)}, rules...), ", ")
}

// ValidateRetention checks the default and per category retention.
func ValidateRetention(days int, perCategory map[string]int) error {
	if days < 1 || days > 3650 {
		return fmt.Errorf("retention must be between 1 and 3650 days")
	}
	for c, d := range perCategory {
		if !CategoryRe.MatchString(c) {
			return fmt.Errorf("invalid category %q", c)
		}
		if d < 1 || d > 3650 {
			return fmt.Errorf("retention must be between 1 and 3650 days")
		}
	}
	return nil
}

// SetRetention changes how long logs are kept, by default and per category.
// With per category rules, rows are deleted from parts that also hold newer
// rows, so dropping whole parts only is switched off.
func (l *Logs) SetRetention(ctx context.Context, days int, perCategory map[string]int) error {
	if err := ValidateRetention(days, perCategory); err != nil {
		return err
	}
	dropParts := 1
	if len(perCategory) > 0 {
		dropParts = 0
	}
	if err := l.Conn.Exec(ctx, fmt.Sprintf("ALTER TABLE %s%s MODIFY SETTING ttl_only_drop_parts = %d", l.local("logs"), l.onCluster(), dropParts)); err != nil {
		return err
	}
	l.mu.Lock()
	l.retention = retention{days: days, perCategory: perCategory}
	move := l.move
	l.mu.Unlock()
	return l.Conn.Exec(ctx, fmt.Sprintf("ALTER TABLE %s%s MODIFY TTL %s", l.local("logs"), l.onCluster(), RetentionTTL(days, perCategory, move)))
}

// CategoryUsage is how much is stored for one category.
type CategoryUsage struct {
	Category string    `json:"category"`
	Rows     uint64    `json:"rows"`
	Oldest   time.Time `json:"oldest"`
}

func (l *Logs) CategoryUsage(ctx context.Context) ([]CategoryUsage, error) {
	rows, err := l.Conn.Query(ctx, "SELECT toString(category) AS c, count(), min(ts) FROM "+l.t("logs")+" GROUP BY c ORDER BY count() DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CategoryUsage{}
	for rows.Next() {
		var u CategoryUsage
		if err := rows.Scan(&u.Category, &u.Rows, &u.Oldest); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
