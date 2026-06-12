package session

import "errors"

// SessionStatus represents the current status of a session.
type SessionStatus string

const (
	StateError              SessionStatus = "error"
	StateInterrupted        SessionStatus = "interrupted"
	StateSuccess            SessionStatus = "success"
	StateSuspendedUserInput SessionStatus = "suspended_user_input"
)

// String returns the string representation of the SessionStatus.
func (s SessionStatus) String() string {
	return string(s)
}

// validTransitions defines which state transitions are allowed.
var validTransitions = map[SessionStatus]map[SessionStatus]bool{
	StateError: {
		StateInterrupted:        true,
		StateSuccess:            true,
		StateSuspendedUserInput: true,
	},
	StateSuspendedUserInput: {
		StateError: true,
	},
	StateInterrupted: {},
	StateSuccess:     {},
}

// ErrInvalidStateTransition is returned when an invalid state transition is attempted.
var ErrInvalidStateTransition = errors.New("invalid state transition")

// Transition attempts to transition the SessionStatus to the target state.
// It returns nil if the transition is allowed, or ErrInvalidStateTransition otherwise.
func (s SessionStatus) Transition(target SessionStatus) error {
	if validTransitions[s][target] {
		return nil
	}
	return ErrInvalidStateTransition
}
