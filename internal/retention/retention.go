// Package retention implements Grandfather-Father-Son retention over backup timestamps.
package retention

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
)

// Item is a backup considered for retention.
type Item struct {
	Name string
	Time time.Time
}

type rule struct {
	name   string
	keep   int
	bucket func(time.Time) string
}

// ParseEvery parses a custom period such as 6h, 90d or 2w.
func ParseEvery(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid period %q", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid period %q", s)
	}
	switch s[len(s)-1] {
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("invalid period %q: unit must be h, d or w", s)
}

// IsEmpty reports whether the policy has no rules, in which case every backup is kept.
func IsEmpty(p v1alpha1.RetentionPolicy) bool {
	for _, v := range []*int32{p.KeepLast, p.Hourly, p.Daily, p.Weekly, p.Monthly, p.Yearly} {
		if v != nil && *v > 0 {
			return false
		}
	}
	return len(p.Custom) == 0
}

// Validate checks the policy.
func Validate(p v1alpha1.RetentionPolicy) error {
	for _, c := range p.Custom {
		if _, err := ParseEvery(c.Every); err != nil {
			return err
		}
		if c.Keep < 1 {
			return fmt.Errorf("custom rule %s: keep must be >= 1", c.Every)
		}
	}
	return nil
}

func rules(p v1alpha1.RetentionPolicy, loc *time.Location) ([]rule, error) {
	var rs []rule
	add := func(name string, n *int32, b func(time.Time) string) {
		if n != nil && *n > 0 {
			rs = append(rs, rule{name, int(*n), b})
		}
	}
	add("last", p.KeepLast, nil)
	add("hourly", p.Hourly, func(t time.Time) string { return t.In(loc).Format("2006-01-02T15") })
	add("daily", p.Daily, func(t time.Time) string { return t.In(loc).Format("2006-01-02") })
	add("weekly", p.Weekly, func(t time.Time) string {
		y, w := t.In(loc).ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	})
	add("monthly", p.Monthly, func(t time.Time) string { return t.In(loc).Format("2006-01") })
	add("yearly", p.Yearly, func(t time.Time) string { return t.In(loc).Format("2006") })
	for _, c := range p.Custom {
		d, err := ParseEvery(c.Every)
		if err != nil {
			return nil, err
		}
		secs := int64(d / time.Second)
		rs = append(rs, rule{"every-" + c.Every, int(c.Keep), func(t time.Time) string {
			_, off := t.In(loc).Zone()
			return strconv.FormatInt((t.Unix()+int64(off))/secs, 10)
		}})
	}
	return rs, nil
}

// Evaluate returns, for every item that must be kept, the rules keeping it.
// Items missing from the result should be deleted. With an empty policy every item is kept
// with no reasons.
func Evaluate(p v1alpha1.RetentionPolicy, items []Item, loc *time.Location) (map[string][]string, error) {
	if loc == nil {
		loc = time.UTC
	}
	keep := make(map[string][]string, len(items))
	if IsEmpty(p) {
		for _, it := range items {
			keep[it.Name] = nil
		}
		return keep, nil
	}
	rs, err := rules(p, loc)
	if err != nil {
		return nil, err
	}
	sorted := append([]Item(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Time.Equal(sorted[j].Time) {
			return sorted[i].Name > sorted[j].Name
		}
		return sorted[i].Time.After(sorted[j].Time)
	})

	for _, r := range rs {
		kept := 0
		last := "\x00"
		for i, it := range sorted {
			if kept >= r.keep {
				break
			}
			b := strconv.Itoa(i)
			if r.bucket != nil {
				b = r.bucket(it.Time)
			}
			if b == last {
				continue
			}
			last = b
			keep[it.Name] = append(keep[it.Name], r.name)
			kept++
		}
	}
	return keep, nil
}
