package db

// SessionCoverageCandidate holds the current state of a session's
// token coverage flags, used as input to ComputeSessionCoverageUpdates.
type SessionCoverageCandidate struct {
	ID                string
	TotalOutputTokens int
	PeakContextTokens int
	HasTotal          bool
	HasPeak           bool
}

// SessionCoverageUpdate holds the computed coverage flags for a
// session that needs updating.
type SessionCoverageUpdate struct {
	ID       string
	HasTotal bool
	HasPeak  bool
}

// ComputeSessionCoverageUpdates computes which sessions need their
// coverage flags updated based on their current state and message-level
// coverage. msgCoverage maps session ID to [hasContext, hasOutput].
// Returns only sessions whose flags would change.
func ComputeSessionCoverageUpdates(
	candidates []SessionCoverageCandidate,
	msgCoverage map[string][2]bool,
) []SessionCoverageUpdate {
	updates := make([]SessionCoverageUpdate, 0)
	for _, c := range candidates {
		coverage := msgCoverage[c.ID]
		newTotal := c.HasTotal || c.TotalOutputTokens != 0 || coverage[1]
		newPeak := c.HasPeak || c.PeakContextTokens != 0 || coverage[0]
		if newTotal == c.HasTotal && newPeak == c.HasPeak {
			continue
		}
		updates = append(updates, SessionCoverageUpdate{
			ID:       c.ID,
			HasTotal: newTotal,
			HasPeak:  newPeak,
		})
	}
	return updates
}
