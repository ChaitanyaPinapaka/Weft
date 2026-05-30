package index

import (
	"reflect"
	"testing"
	"time"
)

func TestAccessHistory(t *testing.T) {
	ix := newIndex(t)
	if h, err := ix.AccessHistory("ghost.html"); err != nil || len(h) != 0 {
		t.Fatalf("never-accessed: want empty, got %v err %v", h, err)
	}
	for _, ts := range []int64{300, 100, 200} {
		if err := ix.LogAccess("a.html", ts); err != nil {
			t.Fatal(err)
		}
	}
	h, err := ix.AccessHistory("a.html")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h, []int64{100, 200, 300}) {
		t.Fatalf("want ascending [100 200 300], got %v", h)
	}
}

func TestAllAccessHistory(t *testing.T) {
	ix := newIndex(t)
	_ = ix.LogAccess("a.html", 100)
	_ = ix.LogAccess("b.html", 50)
	_ = ix.LogAccess("a.html", 90)
	all, err := ix.AllAccessHistory()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all["a.html"], []int64{90, 100}) {
		t.Fatalf("a.html ascending want [90 100], got %v", all["a.html"])
	}
	if !reflect.DeepEqual(all["b.html"], []int64{50}) {
		t.Fatalf("b.html want [50], got %v", all["b.html"])
	}
}

// TestCoAccessCountDistinct is the adversarial-fix regression: the count must
// be invariant to how many times the FOCUS note was opened. A candidate
// co-opened once near a focus opened 5 times must report 1, not 5.
func TestCoAccessCountDistinct(t *testing.T) {
	ix := newIndex(t)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).Unix()
	// focus opened 5 times in a tight window; candidate opened ONCE nearby.
	for i := int64(0); i < 5; i++ {
		_ = ix.LogAccess("focus.html", base+i*10)
	}
	_ = ix.LogAccess("cand.html", base+5)

	cc, err := ix.CoAccessCount("focus.html", 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if cc["cand.html"] != 1 {
		t.Fatalf("co-access count must be invariant to focus frequency: want 1, got %d", cc["cand.html"])
	}

	// Two genuine co-access events for cand should report 2.
	_ = ix.LogAccess("cand.html", base+20)
	cc, _ = ix.CoAccessCount("focus.html", 30*time.Minute)
	if cc["cand.html"] != 2 {
		t.Fatalf("two distinct candidate co-accesses want 2, got %d", cc["cand.html"])
	}

	// Outside the window: no co-access.
	_ = ix.LogAccess("far.html", base+10*60*60)
	cc, _ = ix.CoAccessCount("focus.html", 30*time.Minute)
	if _, ok := cc["far.html"]; ok {
		t.Fatalf("far.html is outside the window, should not be co-accessed: %v", cc)
	}
}

func TestPruneAccessLog(t *testing.T) {
	ix := newIndex(t)
	_ = ix.LogAccess("a.html", 100)
	_ = ix.LogAccess("a.html", 500)
	_ = ix.LogAccess("b.html", 900)

	if err := ix.PruneAccessLog(500); err != nil { // drop ts < 500
		t.Fatal(err)
	}
	all, _ := ix.AllAccessHistory()
	if !reflect.DeepEqual(all["a.html"], []int64{500}) {
		t.Fatalf("a.html should keep only ts>=500, got %v", all["a.html"])
	}
	if !reflect.DeepEqual(all["b.html"], []int64{900}) {
		t.Fatalf("b.html should be untouched, got %v", all["b.html"])
	}
}

func TestRecentAccesses(t *testing.T) {
	ix := newIndex(t)
	_ = ix.LogAccess("a.html", 100)
	_ = ix.LogAccess("b.html", 200)
	_ = ix.LogAccess("c.html", 300)

	ra, err := ix.RecentAccesses(150)
	if err != nil {
		t.Fatal(err)
	}
	// since=150 excludes a.html(100); DESC order => c then b.
	if len(ra) != 2 || ra[0].Path != "c.html" || ra[1].Path != "b.html" {
		t.Fatalf("want [c@300 b@200] desc, got %v", ra)
	}
}
