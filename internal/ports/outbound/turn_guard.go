package outbound

import "context"

// TurnGuard serializes the turns of one conversation. Two turns of the same
// conversation must never run at once: both would read the same history and
// append their messages interleaved, corrupting the context of every later
// turn.
//
// An in-process guard is enough while one instance serves everything; with
// several instances the guard must be shared (e.g. Redis) alongside a shared
// ConversationStore.
type TurnGuard interface {
	// TryAcquire claims the turn for conversationID without waiting.
	// acquired is false when another turn of that conversation is running;
	// release is then nil. Otherwise the caller must call release exactly
	// once when its turn ends, whether or not ctx is still live.
	//
	// Implementations whose claim can outlive a crashed holder must expire it
	// on their own, after more than a request's timeout.
	TryAcquire(ctx context.Context, conversationID string) (release func(), acquired bool, err error)
}
