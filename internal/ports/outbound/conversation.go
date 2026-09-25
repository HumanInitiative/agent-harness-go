package outbound

import (
	"context"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/conversation"
)

// ConversationStore persists conversation turns so a follow-up message can
// continue an earlier exchange. Swapping the in-memory adapter for a durable
// one (e.g. Postgres) only requires a new adapter behind this port.
//
// Implementations may bound what they retain (drop old messages, expire idle
// conversations), so History can legitimately return less than was ever
// appended.
type ConversationStore interface {
	// History returns the messages retained for conversationID, oldest
	// first. An unknown or expired conversationID returns an empty slice,
	// not an error.
	History(ctx context.Context, conversationID string) ([]conversation.Message, error)

	// Append records messages at the end of conversationID's history,
	// creating the conversation if it does not exist yet.
	Append(ctx context.Context, conversationID string, messages ...conversation.Message) error
}
