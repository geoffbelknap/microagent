package operation

// ProgressStatus is the stable state of a long-running operation update.
// An empty status is treated as running so existing producers can adopt the
// shared event incrementally without fabricating terminal state.
type ProgressStatus string

const (
	ProgressRunning   ProgressStatus = "running"
	ProgressSucceeded ProgressStatus = "succeeded"
	ProgressFailed    ProgressStatus = "failed"
	ProgressCanceled  ProgressStatus = "canceled"
)

// ProgressEvent describes observable work without prescribing how an adapter
// presents it. Operation identifies one sequential unit of work; Phase is a
// stable machine-readable step within it. Label and Message are safe human
// summaries. Counts and bytes are optional determinate progress dimensions.
type ProgressEvent struct {
	Operation     string         `json:"operation,omitempty"`
	Phase         string         `json:"phase,omitempty"`
	Label         string         `json:"label,omitempty"`
	Message       string         `json:"message,omitempty"`
	Current       int64          `json:"current,omitempty"`
	Total         int64          `json:"total,omitempty"`
	Bytes         int64          `json:"bytes,omitempty"`
	TotalBytes    int64          `json:"total_bytes,omitempty"`
	ElapsedMs     int64          `json:"elapsed_ms,omitempty"`
	Indeterminate bool           `json:"indeterminate,omitempty"`
	Status        ProgressStatus `json:"status,omitempty"`
	Error         string         `json:"error,omitempty"`
}

// Terminal reports whether the event ends its operation.
func (e ProgressEvent) Terminal() bool {
	switch e.Status {
	case ProgressSucceeded, ProgressFailed, ProgressCanceled:
		return true
	default:
		return false
	}
}

// ProgressFunc receives typed operation progress updates.
type ProgressFunc func(ProgressEvent)
