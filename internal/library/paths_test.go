package library

import (
	"strings"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"setup_game-1.bin", "setup_game-1.bin"},
		{"../bad\\name:?.exe", "bad name .exe"},
		{"...", "untitled"},
		{"", "untitled"},
		{"setup_" + strings.Repeat("é", 80) + "-2.bin", "setup_" + strings.Repeat("é", 80) + "-2.bin"},
	} {
		if got := SanitizeFilename(tc.input); got != tc.want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
