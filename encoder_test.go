package reisen

import (
	"testing"
)

func TestFindEncoder(t *testing.T) {
	// Test that we can find common encoders
	tests := []struct {
		name      string
		shouldErr bool
	}{
		{"libx264", false},  // Common video encoder
		{"aac", false},      // Common audio encoder
		{"nonexistent", true},
	}

	for _, tc := range tests {
		enc, err := findEncoder(tc.name)
		if tc.shouldErr {
			if err == nil {
				t.Errorf("%s: expected error", tc.name)
			}
		} else {
			if err != nil {
				t.Errorf("%s: unexpected error: %v", tc.name, err)
			}
			if enc == nil {
				t.Errorf("%s: expected non-nil encoder", tc.name)
			}
		}
	}
}
