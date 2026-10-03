package main

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestHelpView(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		sideBySide    bool
		cut           bool
	}{
		{"wide and tall", 160, 60, true, false},
		{"narrow stacks", 60, 80, false, false},
		{"short cuts", 160, 10, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := helpView(tc.width, tc.height)
			lines := strings.Split(got, "\n")
			if len(lines) > tc.height {
				t.Errorf("%d lines for height %d", len(lines), tc.height)
			}
			if w := lipgloss.Width(got); w > tc.width && !tc.cut {
				t.Errorf("width %d for %d columns", w, tc.width)
			}
			if side := strings.Contains(lines[0], "Commands"); side != tc.sideBySide {
				t.Errorf("side by side = %v, want %v", side, tc.sideBySide)
			}
			if cut := strings.Contains(got, "more in the README"); cut != tc.cut {
				t.Errorf("cut = %v, want %v", cut, tc.cut)
			}
		})
	}
}
