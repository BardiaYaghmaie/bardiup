package retention

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
)

func i32(v int32) *int32 { return &v }

func hourlyItems(start time.Time, n int, step time.Duration) []Item {
	items := make([]Item, n)
	for i := range n {
		t := start.Add(time.Duration(i) * step)
		items[i] = Item{Name: t.Format("2006-01-02T15:04"), Time: t}
	}
	return items
}

func kept(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestEmptyPolicyKeepsEverything(t *testing.T) {
	items := hourlyItems(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 50, time.Hour)
	got, err := Evaluate(v1alpha1.RetentionPolicy{}, items, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Fatalf("kept %d, want 50", len(got))
	}
}

func TestKeepLast(t *testing.T) {
	items := hourlyItems(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 10, time.Hour)
	got, _ := Evaluate(v1alpha1.RetentionPolicy{KeepLast: i32(3)}, items, nil)
	want := []string{"2026-01-01T07:00", "2026-01-01T08:00", "2026-01-01T09:00"}
	if !reflect.DeepEqual(kept(got), want) {
		t.Fatalf("got %v want %v", kept(got), want)
	}
}

func TestDailyKeepsNewestPerDay(t *testing.T) {
	// Every 6h for 10 days.
	items := hourlyItems(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), 40, 6*time.Hour)
	got, _ := Evaluate(v1alpha1.RetentionPolicy{Daily: i32(3)}, items, nil)
	want := []string{"2026-03-08T18:00", "2026-03-09T18:00", "2026-03-10T18:00"}
	if !reflect.DeepEqual(kept(got), want) {
		t.Fatalf("got %v want %v", kept(got), want)
	}
	for _, r := range got {
		if !reflect.DeepEqual(r, []string{"daily"}) {
			t.Fatalf("reasons %v", r)
		}
	}
}

func TestDailySkipsGaps(t *testing.T) {
	// A missing day does not consume a slot: N rules count days that have backups.
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	items := []Item{
		{"d1", base},
		{"d2", base.Add(24 * time.Hour)},
		{"d5", base.Add(4 * 24 * time.Hour)},
	}
	got, _ := Evaluate(v1alpha1.RetentionPolicy{Daily: i32(3)}, items, nil)
	if len(got) != 3 {
		t.Fatalf("got %v", kept(got))
	}
}

func TestGFSOverOneYear(t *testing.T) {
	// One backup a day at 02:00 for 400 days.
	start := time.Date(2025, 1, 1, 2, 0, 0, 0, time.UTC)
	items := hourlyItems(start, 400, 24*time.Hour)
	p := v1alpha1.RetentionPolicy{Daily: i32(7), Weekly: i32(4), Monthly: i32(12), Yearly: i32(3)}
	got, err := Evaluate(p, items, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, reasons := range got {
		for _, r := range reasons {
			counts[r]++
		}
	}
	want := map[string]int{"daily": 7, "weekly": 4, "monthly": 12, "yearly": 2}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("rule counts %v want %v", counts, want)
	}
	// Overlaps mean far fewer than 7+4+12+2 distinct backups.
	if len(got) >= 25 || len(got) < 12 {
		t.Fatalf("kept %d backups: %v", len(got), kept(got))
	}
	// The newest backup is always kept.
	newest := items[len(items)-1].Name
	if _, ok := got[newest]; !ok {
		t.Fatal("newest backup was not kept")
	}
	// Monthly keeps the last backup of each month.
	if _, ok := got["2025-12-31T02:00"]; !ok {
		t.Fatalf("end of December not kept: %v", kept(got))
	}
	// Yearly keeps the last backup of 2025.
	if r := got["2025-12-31T02:00"]; !contains(r, "yearly") {
		t.Fatalf("2025 yearly missing, reasons %v", r)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestHourly(t *testing.T) {
	items := hourlyItems(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 24, 15*time.Minute)
	got, _ := Evaluate(v1alpha1.RetentionPolicy{Hourly: i32(2)}, items, nil)
	want := []string{"2026-01-01T04:45", "2026-01-01T05:45"}
	if !reflect.DeepEqual(kept(got), want) {
		t.Fatalf("got %v want %v", kept(got), want)
	}
}

func TestTimeZoneShiftsDayBoundaries(t *testing.T) {
	// 23:30 UTC and 00:30 UTC are different UTC days but the same day in Tehran (+03:30).
	tehran, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	items := []Item{
		{"a", time.Date(2026, 6, 1, 23, 30, 0, 0, time.UTC)},
		{"b", time.Date(2026, 6, 2, 0, 30, 0, 0, time.UTC)},
	}
	p := v1alpha1.RetentionPolicy{Daily: i32(5)}
	utc, _ := Evaluate(p, items, time.UTC)
	local, _ := Evaluate(p, items, tehran)
	if len(utc) != 2 || len(local) != 1 {
		t.Fatalf("utc kept %v, tehran kept %v", kept(utc), kept(local))
	}
}

func TestCustomQuarterly(t *testing.T) {
	items := hourlyItems(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), 800, 24*time.Hour)
	p := v1alpha1.RetentionPolicy{Custom: []v1alpha1.CustomRetention{{Every: "90d", Keep: 4}}}
	got, err := Evaluate(p, items, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("kept %d: %v", len(got), kept(got))
	}
	for _, r := range got {
		if r[0] != "every-90d" {
			t.Fatalf("reason %v", r)
		}
	}
	// Older kept backups are exactly one period apart; the newest sits in a partial period.
	names := kept(got)
	var times []time.Time
	for _, n := range names {
		tm, _ := time.Parse("2006-01-02T15:04", n)
		times = append(times, tm)
	}
	for i := 1; i < len(times)-1; i++ {
		if d := times[i].Sub(times[i-1]); d != 90*24*time.Hour {
			t.Errorf("gap %v between %v and %v", d, times[i-1], times[i])
		}
	}
}

func TestReasonsAccumulate(t *testing.T) {
	items := []Item{{"only", time.Now()}}
	p := v1alpha1.RetentionPolicy{KeepLast: i32(1), Daily: i32(1), Weekly: i32(1), Monthly: i32(1), Yearly: i32(1), Hourly: i32(1),
		Custom: []v1alpha1.CustomRetention{{Every: "6h", Keep: 1}}}
	got, _ := Evaluate(p, items, nil)
	want := []string{"last", "hourly", "daily", "weekly", "monthly", "yearly", "every-6h"}
	if !reflect.DeepEqual(got["only"], want) {
		t.Fatalf("got %v", got["only"])
	}
}

func TestDeterministicOnEqualTimes(t *testing.T) {
	now := time.Now()
	items := []Item{{"b", now}, {"a", now}, {"c", now}}
	for range 10 {
		got, _ := Evaluate(v1alpha1.RetentionPolicy{KeepLast: i32(1)}, items, nil)
		if _, ok := got["c"]; !ok || len(got) != 1 {
			t.Fatalf("got %v", kept(got))
		}
	}
}

func TestParseEvery(t *testing.T) {
	ok := map[string]time.Duration{"1h": time.Hour, "90d": 90 * 24 * time.Hour, "2w": 14 * 24 * time.Hour}
	for in, want := range ok {
		if got, err := ParseEvery(in); err != nil || got != want {
			t.Errorf("ParseEvery(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "d", "0d", "-1d", "5m", "1.5d", "x1d"} {
		if _, err := ParseEvery(in); err == nil {
			t.Errorf("ParseEvery(%q) should fail", in)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(v1alpha1.RetentionPolicy{Custom: []v1alpha1.CustomRetention{{Every: "5m", Keep: 1}}}); err == nil {
		t.Error("expected invalid period")
	}
	if err := Validate(v1alpha1.RetentionPolicy{Custom: []v1alpha1.CustomRetention{{Every: "5d", Keep: 0}}}); err == nil {
		t.Error("expected invalid keep")
	}
	if err := Validate(v1alpha1.RetentionPolicy{Daily: i32(7)}); err != nil {
		t.Error(err)
	}
	if !IsEmpty(v1alpha1.RetentionPolicy{Daily: i32(0)}) {
		t.Error("zero rule should count as empty")
	}
}

func ExampleEvaluate() {
	base := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	var items []Item
	for d := range 60 {
		t := base.AddDate(0, 0, d)
		items = append(items, Item{Name: t.Format("Jan02"), Time: t})
	}
	keep, _ := Evaluate(v1alpha1.RetentionPolicy{Daily: i32(3), Monthly: i32(3)}, items, nil)
	for _, n := range []string{"Mar01", "Feb28", "Feb27", "Jan31", "Jan30"} {
		fmt.Println(n, keep[n])
	}
	// Output:
	// Mar01 [daily monthly]
	// Feb28 [daily monthly]
	// Feb27 [daily]
	// Jan31 [monthly]
	// Jan30 []
}

// FuzzEvaluate checks invariants over random backup histories and policies.
func FuzzEvaluate(f *testing.F) {
	f.Add(int64(1), uint8(50), uint8(3), uint8(7), uint8(4), uint8(12), uint8(2), uint16(6))
	f.Fuzz(func(t *testing.T, seed int64, n, last, daily, weekly, monthly, yearly uint8, everyHours uint16) {
		start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		var items []Item
		x := uint64(seed)
		at := start
		for i := range int(n) {
			x = x*6364136223846793005 + 1442695040888963407
			at = at.Add(time.Duration(x%(72*3600)) * time.Second)
			items = append(items, Item{Name: fmt.Sprintf("b%03d", i), Time: at})
		}
		p := v1alpha1.RetentionPolicy{KeepLast: i32(int32(last % 10)), Daily: i32(int32(daily % 10)), Weekly: i32(int32(weekly % 10)),
			Monthly: i32(int32(monthly % 10)), Yearly: i32(int32(yearly % 5))}
		if everyHours > 0 {
			p.Custom = []v1alpha1.CustomRetention{{Every: fmt.Sprintf("%dh", everyHours), Keep: 3}}
		}
		keep, err := Evaluate(p, items, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 0 {
			if _, ok := keep[items[len(items)-1].Name]; !ok {
				t.Fatal("newest backup must always be kept")
			}
		}
		// Evaluating the kept set again keeps all of it: retention is stable, it never cascades.
		var survivors []Item
		for _, it := range items {
			if _, ok := keep[it.Name]; ok {
				survivors = append(survivors, it)
			}
		}
		again, _ := Evaluate(p, survivors, time.UTC)
		if len(again) != len(survivors) {
			t.Fatalf("not stable: %d survivors, %d kept on re-evaluation", len(survivors), len(again))
		}
		limit := int(last%10) + int(daily%10) + int(weekly%10) + int(monthly%10) + int(yearly%5)
		if everyHours > 0 {
			limit += 3
		}
		if len(keep) > limit && !IsEmpty(p) {
			t.Fatalf("kept %d > sum of rules %d", len(keep), limit)
		}
	})
}
