// Package progression defines the XP curve and the rank bands that unlock
// scraping threads. Threads are no longer purchased: a user's level (derived
// from total XP) determines how many threads they get.
package progression

import "math"

// MaxLevel is the highest level band (the top rank). Levels keep growing past
// it, but ranks, threads and the other benefits stop at the top band.
const MaxLevel = 100

// Rank is a level band that grants a thread count.
type Rank struct {
	Key      string
	MinLevel int
	MaxLevel int
	Threads  int
}

// Ranks are the ten progression bands, from Novice (level 1) to Legend
// (level 100). Key is a stable English identifier the UI translates.
var Ranks = []Rank{
	{"novice", 1, 10, 4},
	{"amateur", 11, 20, 5},
	{"explorer", 21, 30, 6},
	{"collector", 31, 40, 7},
	{"curator", 41, 50, 8},
	{"conservator", 51, 60, 9},
	{"archivist", 61, 70, 10},
	{"scholar", 71, 80, 12},
	{"master", 81, 90, 14},
	{"legend", 91, 100, 16},
}

// MaxThreads is the thread count of the top rank.
func MaxThreads() int { return Ranks[len(Ranks)-1].Threads }

// XPForLevel returns the total XP required to reach the given level. Level 1 is
// the starting level (0 XP); higher levels follow round(level^2.5 * 3), so the
// curve is easy at first and steep near the top (level 100 = 300,000 XP). There
// is no upper bound: the level keeps growing past the top rank.
func XPForLevel(level int) int {
	if level <= 1 {
		return 0
	}
	return int(math.Round(math.Pow(float64(level), 2.5) * 3))
}

// LevelForXP returns the level for a total XP amount. It is unbounded, so a user
// keeps leveling up past MaxLevel (the benefits stop at the top rank instead).
func LevelForXP(xp int) int {
	if xp <= 0 {
		return 1
	}
	// Invert the curve (level ~ (xp/3)^(1/2.5)) and correct the rounding both
	// ways, so the loop stays constant-time for very large XP values.
	level := int(math.Pow(float64(xp)/3.0, 1.0/2.5))
	if level < 1 {
		level = 1
	}
	for level > 1 && XPForLevel(level) > xp {
		level--
	}
	for xp >= XPForLevel(level+1) {
		level++
	}
	return level
}

// ThreadsForLevel returns the thread count unlocked at a level.
func ThreadsForLevel(level int) int {
	if level < 1 {
		level = 1
	}
	for _, r := range Ranks {
		if level >= r.MinLevel && level <= r.MaxLevel {
			return r.Threads
		}
	}
	return Ranks[len(Ranks)-1].Threads
}

// RankForLevel returns the rank key for a level.
func RankForLevel(level int) string {
	if level < 1 {
		level = 1
	}
	for _, r := range Ranks {
		if level >= r.MinLevel && level <= r.MaxLevel {
			return r.Key
		}
	}
	return Ranks[len(Ranks)-1].Key
}
