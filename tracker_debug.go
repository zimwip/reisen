//go:build leakcheck

package reisen

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

var (
	trackerMu sync.Mutex
	tracked   = make(map[uintptr]LeakRecord)
)

func callerStack(skip int) string {
	var pcs [8]uintptr
	n := runtime.Callers(skip+2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	result := ""
	for {
		frame, more := frames.Next()
		result += fmt.Sprintf("  %s:%d %s\n", frame.File, frame.Line, frame.Function)
		if !more {
			break
		}
	}
	return result
}

// trackAlloc records a C resource allocation for leak detection.
func trackAlloc(kind ResourceKind, ptr unsafe.Pointer) {
	if ptr == nil {
		return
	}
	trackerMu.Lock()
	defer trackerMu.Unlock()
	tracked[uintptr(ptr)] = LeakRecord{
		Kind:  kind,
		Addr:  uintptr(ptr),
		Stack: callerStack(2),
	}
}

// trackFree records that a C resource has been freed.
func trackFree(ptr unsafe.Pointer) {
	if ptr == nil {
		return
	}
	trackerMu.Lock()
	defer trackerMu.Unlock()
	delete(tracked, uintptr(ptr))
}

// DumpLeaks returns all tracked resources that have not been freed.
// Useful in tests or at application shutdown to verify no leaks.
func DumpLeaks() []LeakRecord {
	trackerMu.Lock()
	defer trackerMu.Unlock()
	result := make([]LeakRecord, 0, len(tracked))
	for _, rec := range tracked {
		result = append(result, rec)
	}
	return result
}

// ResetTracker clears all tracking state. Useful between test runs.
func ResetTracker() {
	trackerMu.Lock()
	defer trackerMu.Unlock()
	tracked = make(map[uintptr]LeakRecord)
}

// TrackedCount returns the number of currently tracked (un-freed) resources.
func TrackedCount() int {
	trackerMu.Lock()
	defer trackerMu.Unlock()
	return len(tracked)
}
