package outbound

import (
	"errors"
	"time"
)

var (
	// ErrModelUnavailable means the model provider is overloaded, rate
	// limiting us, or down. It is transient: the same request may succeed
	// later. Match it with errors.Is; use errors.As with
	// *ModelUnavailableError to read the suggested retry delay.
	ErrModelUnavailable = errors.New("model provider unavailable")

	// ErrToolTurnLimit means the model kept requesting tools until
	// GenerateRequest.MaxTurns was exhausted without producing an answer.
	ErrToolTurnLimit = errors.New("tool call turn limit exceeded")
)

// ModelUnavailableError carries the provider's suggested retry delay, when
// it gave one, alongside the underlying cause.
type ModelUnavailableError struct {
	// RetryAfter is the delay the provider asked callers to wait. Zero
	// means the provider did not say.
	RetryAfter time.Duration
	Cause      error
}

func (e *ModelUnavailableError) Error() string {
	if e.Cause == nil {
		return ErrModelUnavailable.Error()
	}
	return ErrModelUnavailable.Error() + ": " + e.Cause.Error()
}

func (e *ModelUnavailableError) Unwrap() error { return e.Cause }

// Is makes errors.Is(err, ErrModelUnavailable) match this type.
func (e *ModelUnavailableError) Is(target error) bool { return target == ErrModelUnavailable }
