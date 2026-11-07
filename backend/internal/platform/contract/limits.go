package contract

// MaxReleaseBytes bounds the combined immutable manifest before publication and
// during runtime validation. Per-event limits do not bound a multi-event release.
const MaxReleaseBytes = 16 * 1024 * 1024
