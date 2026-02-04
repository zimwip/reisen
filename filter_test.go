package reisen

import (
	"testing"
)

func TestFilterContextType(t *testing.T) {
	// Basic type existence test
	var fc *filterContext
	if fc != nil {
		t.Error("expected nil")
	}
}

func TestBuildVideoFilterSpec(t *testing.T) {
	// Test filter spec building helper
	spec := buildFilterSpec("scale=1280:-2", "yuv420p")
	if spec == "" {
		t.Error("expected non-empty filter spec")
	}
}
