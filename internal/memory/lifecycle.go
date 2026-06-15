package memory

import (
	"fmt"
	"time"
)

// validTransitions defines the allowed state transitions.
//
// Challenge is decisive: since v4.5.0 a challenge that passes BFT consensus
// deprecates the memory in one step (committed→deprecated) — see
// processMemoryChallenge in internal/abci/app.go. There is no reachable
// `challenged` state, so it carries no transition edges here. The StatusChallenged
// enum is retained (model.go) only for legacy on-disk rows from <v4.5.0, which the
// ResolveChallengedMemories boot migration (internal/store/sqlite.go) sweeps to
// deprecated. `deprecated` is terminal.
var validTransitions = map[MemoryStatus][]MemoryStatus{
	StatusProposed:  {StatusValidated, StatusDeprecated},
	StatusValidated: {StatusCommitted, StatusDeprecated},
	StatusCommitted: {StatusDeprecated},
}

// ValidTransition checks if a state transition is allowed.
func ValidTransition(from, to MemoryStatus) bool {
	allowed, ok := validTransitions[from]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == to {
			return true
		}
	}
	return false
}

// Transition attempts to transition a memory record to a new status.
func Transition(record *MemoryRecord, to MemoryStatus, now time.Time) error {
	if !ValidTransition(record.Status, to) {
		return fmt.Errorf("invalid transition from %s to %s", record.Status, to)
	}

	record.Status = to

	switch to {
	case StatusCommitted:
		record.CommittedAt = &now
	case StatusDeprecated:
		record.DeprecatedAt = &now
	}

	return nil
}
