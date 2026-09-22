package service

import "github.com/slchris/qubes-air/console/internal/models"

// isRenderable reports whether a qube is one the orchestrator may act on.
//
// A released qube is still "renderable": it no longer has a compute instance,
// but its data disk (protected in qube_infra) still exists and must remain
// addressable so a purge can find and destroy it. A purged qube owns nothing:
// its disk is gone and its record is history, so it is not resolvable. A pending
// qube has no infrastructure yet, and a zoneless one has nowhere to be placed.
func isRenderable(q *models.Qube) bool {
	return q.ZoneID != "" && q.Status != models.QubeStatusPending && q.Status != models.QubeStatusPurged
}

// computeRunning maps a qube's status onto "should a compute instance exist".
//
// Transient statuses report the state being moved TOWARD, because the operation
// is decided immediately before the move.
//
// It is also the console's answer to "does this qube have a compute instance at
// all", and the probe and certificate-reissue paths ask it here rather than
// keeping their own list of statuses (see ProbeAgent and
// reissueIdentityForResume in qube_service.go). One predicate, because a
// disagreement between "there is no VM to talk to" and "there is one" is how a
// qube ends up being probed at an address DHCP has since handed to somebody
// else, or having its identity file rewritten underneath a live instance.
func computeRunning(status models.QubeStatus) bool {
	switch status {
	case models.QubeStatusRunning, models.QubeStatusCreating, models.QubeStatusResuming:
		return true
	default:
		// stopped, suspended, released, suspending, deleting, error
		return false
	}
}
