package main

import (
	"math/bits"
	"testing"
)

func TestNewKills(t *testing.T) {
	makeRow := func(pattern uint64) detection {
		var row detection
		for y := 3; y < 14; y++ {
			row.victim.rows[y][0] = pattern
			row.victim.count += bits.OnesCount64(pattern)
		}
		return row
	}
	first := makeRow(0x11111111)
	shiftedFirst := makeRow(0x22222222)
	second := makeRow(0xF000000000000000)
	for _, tc := range []struct {
		name              string
		previous, current []detection
		want              int
	}{
		{"first appearance", nil, []detection{first}, 1},
		{"lingering name shifts in the feed", []detection{first}, []detection{shiftedFirst}, 0},
		{"new victim while first row lingers", []detection{first}, []detection{second, shiftedFirst}, 1},
		{"another row for the same victim", []detection{first}, []detection{first, first}, 1},
		{"new victim replaces old row", []detection{first}, []detection{second}, 1},
		{"same victim after an empty sample", nil, []detection{first}, 1},
		{"both names already seen", []detection{first, second}, []detection{second, first}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newKills(tc.previous, tc.current); got != tc.want {
				t.Errorf("newKills returned %d, want %d", got, tc.want)
			}
		})
	}
	t.Run("short OCR dropout", func(t *testing.T) {
		tracker := killTracker{maxGap: 4}
		if got := tracker.update(1, []detection{first}); got != 1 {
			t.Fatalf("first appearance returned %d", got)
		}
		tracker.update(2, nil)
		tracker.update(3, nil)
		if got := tracker.update(4, []detection{shiftedFirst}); got != 0 {
			t.Errorf("lingering row after dropout returned %d", got)
		}
		if got := tracker.update(9, []detection{first}); got != 1 {
			t.Errorf("row after timeout returned %d", got)
		}
	})
}

func TestVictimFingerprintIgnoresBrightEdge(t *testing.T) {
	const y = 8
	gray := make([]byte, frameWidth*(y+23))
	// Paint two lines of strokes with uneven gaps near the right of the HUD.
	for row := 3; row < 15; row++ {
		for x := 580; x <= 635; x++ {
			if (x-580)%11 < 2 || (x-580)%23 <= row/3 || (x == 635 && row < 10) {
				gray[(y+row)*frameWidth+x] = 230
			}
		}
	}
	before := detection{victim: victimFingerprint(gray, y)}
	for row := 0; row < 15; row++ {
		for x := 643; x <= 644; x++ {
			gray[(y+row)*frameWidth+x] = 230
		}
	}
	after := detection{victim: victimFingerprint(gray, y)}
	tracker := killTracker{maxGap: 6}
	if got := tracker.update(1, []detection{before}); got != 1 {
		t.Fatalf("first keyframe returned %d new kills, want 1", got)
	}
	if got := tracker.update(5.167, []detection{after}); got != 0 {
		t.Fatalf("same victim with a bright edge returned %d new kills, want 0", got)
	}
}
