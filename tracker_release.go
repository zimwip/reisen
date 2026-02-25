//go:build !leakcheck

package reisen

import "unsafe"

// No-op implementations for production builds.
// These are optimized away by the compiler.

func trackAlloc(_ ResourceKind, _ unsafe.Pointer) {}
func trackFree(_ unsafe.Pointer)                  {}

// DumpLeaks always returns nil in production builds.
func DumpLeaks() []LeakRecord { return nil }

// ResetTracker is a no-op in production builds.
func ResetTracker() {}

// TrackedCount always returns 0 in production builds.
func TrackedCount() int { return 0 }
