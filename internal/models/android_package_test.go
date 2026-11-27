package models

import (
	"strings"
	"testing"
)

func TestValidAndroidPackage(t *testing.T) {
	for p, want := range map[string]bool{
		"com.innersloth.spacemafia":       true,
		"com.LamestarGames.Pinball":       true,
		"com.game_studio.app2":            true,
		"nodots":                          false,
		"com..bad":                        false,
		"com.bad-name":                    false,
		"1com.bad":                        false,
		"com.bad.":                        false,
		"com." + strings.Repeat("a", 252): false,
	} {
		if got := ValidAndroidPackage(p); got != want {
			t.Errorf("ValidAndroidPackage(%q) = %v, want %v", p, got, want)
		}
	}
}
