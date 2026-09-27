package progression

import "testing"

func TestXPForLevel(t *testing.T) {
	if got := XPForLevel(1); got != 0 {
		t.Errorf("XPForLevel(1) = %d, want 0", got)
	}
	if got := XPForLevel(100); got != 300000 {
		t.Errorf("XPForLevel(100) = %d, want 300000", got)
	}
	// The curve must be strictly increasing.
	prev := -1
	for l := 1; l <= MaxLevel; l++ {
		if x := XPForLevel(l); x <= prev {
			t.Fatalf("XPForLevel not increasing at level %d: %d <= %d", l, x, prev)
		}
		prev = XPForLevel(l)
	}
}

func TestLevelForXPRoundTrip(t *testing.T) {
	for _, level := range []int{1, 5, 10, 25, 50, 75, 100} {
		xp := XPForLevel(level)
		if got := LevelForXP(xp); got != level {
			t.Errorf("LevelForXP(XPForLevel(%d)=%d) = %d, want %d", level, xp, got, level)
		}
	}
	if got := LevelForXP(0); got != 1 {
		t.Errorf("LevelForXP(0) = %d, want 1", got)
	}
}

func TestLevelForXPUnbounded(t *testing.T) {
	// Past the top rank the level keeps growing while the benefits stay capped.
	if got := LevelForXP(XPForLevel(101)); got != 101 {
		t.Errorf("LevelForXP(XPForLevel(101)) = %d, want 101", got)
	}
	if got := LevelForXP(XPForLevel(150)); got != 150 {
		t.Errorf("LevelForXP(XPForLevel(150)) = %d, want 150", got)
	}
	if got := ThreadsForLevel(101); got != MaxThreads() {
		t.Errorf("ThreadsForLevel(101) = %d, want %d", got, MaxThreads())
	}
	if got := ThreadsForLevel(150); got != 16 {
		t.Errorf("ThreadsForLevel(150) = %d, want 16", got)
	}
	if got := RankForLevel(150); got != "legend" {
		t.Errorf("RankForLevel(150) = %q, want legend", got)
	}
}

func TestThreadsForLevel(t *testing.T) {
	cases := map[int]int{1: 4, 10: 4, 11: 5, 20: 5, 50: 8, 90: 14, 91: 16, 100: 16}
	for level, want := range cases {
		if got := ThreadsForLevel(level); got != want {
			t.Errorf("ThreadsForLevel(%d) = %d, want %d", level, got, want)
		}
	}
}

func TestRankForLevel(t *testing.T) {
	if got := RankForLevel(1); got != "novice" {
		t.Errorf("RankForLevel(1) = %q, want novice", got)
	}
	if got := RankForLevel(100); got != "legend" {
		t.Errorf("RankForLevel(100) = %q, want legend", got)
	}
}
