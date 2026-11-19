package catalog

// Release lifecycle state vocabulary. Releases are born published in the
// current implementation; draft/validating/publishing/failed remain
// contract-published states reserved for the future release workflow, and
// the migration 002 storage CHECK likewise only permits published today.
const ReleaseStatePublished = "published"

// ReleaseStates is the Go-produced subset of the Release.state enum.
var ReleaseStates = []string{ReleaseStatePublished}

// Release acceleration vocabulary. A release starts not_requested; the
// external delivery subsystem moves the delivery record to building and
// later ready/failed, which the Go read model observes through the join.
// ready/failed have no Go observation point yet and stay contract-only.
const (
	ReleaseAccelerationNotRequested = "not_requested"
	ReleaseAccelerationBuilding     = "building"
)

// ReleaseAccelerationStates is the Go-observable subset of the
// Release.acceleration_state enum. Raw SQL keeps its own literals because
// the delivery transitions run inside query text against the migration 003
// CHECK constraint.
var ReleaseAccelerationStates = []string{
	ReleaseAccelerationNotRequested,
	ReleaseAccelerationBuilding,
}
