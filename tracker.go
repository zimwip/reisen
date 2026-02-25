package reisen

// Resource tracker for detecting C memory leaks at runtime.
//
// Usage: build with -tags leakcheck to enable tracking.
// In production builds (default), all tracker calls are no-ops.
//
// Example:
//
//	media, _ := reisen.NewMedia("video.mp4")
//	defer media.Close()
//	// ... use media ...
//	leaks := reisen.DumpLeaks() // returns all un-freed resources (empty if no leaks)

// ResourceKind identifies the type of tracked C resource.
type ResourceKind string

const (
	ResAVFormatContext ResourceKind = "AVFormatContext"
	ResAVCodecContext  ResourceKind = "AVCodecContext"
	ResAVFrame         ResourceKind = "AVFrame"
	ResAVPacket        ResourceKind = "AVPacket"
	ResSwsContext      ResourceKind = "SwsContext"
	ResSwrContext      ResourceKind = "SwrContext"
	ResAVIOContext     ResourceKind = "AVIOContext"
	ResAVFilterGraph   ResourceKind = "AVFilterGraph"
	ResAVBuffer        ResourceKind = "AVBuffer"
)

// LeakRecord describes a tracked resource that has not been freed.
type LeakRecord struct {
	Kind  ResourceKind
	Addr  uintptr
	Stack string // call stack at allocation time (when available)
}
