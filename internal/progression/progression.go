// Package progression defines the XP curve and the rank bands that unlock
// scraping threads. Threads are no longer purchased: a user's level (derived
// from total XP) determines how many threads they get.
package progression

import "math"

// MaxLevel is the highest reachable level.
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
// curve is easy at first and steep near the top (level 100 = 300,000 XP).
func XPForLevel(level int) int {
	if level <= 1 {
		return 0
	}
	if level > MaxLevel {
		level = MaxLevel
	}
	return int(math.Round(math.Pow(float64(level), 2.5) * 3))
}

// LevelForXP returns the level (1..MaxLevel) for a total XP amount.
func LevelForXP(xp int) int {
	level := 1
	for level < MaxLevel && xp >= XPForLevel(level+1) {
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
