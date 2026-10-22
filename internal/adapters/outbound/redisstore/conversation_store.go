package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/conversation"
)

// ConversationOptions bounds what a ConversationStore retains. Every field is
// required.
type ConversationOptions struct {
	MaxMessagesPerConversation int
	// TTL is renewed by every append; a conversation idle longer is gone.
	TTL time.Duration
	// KeyPrefix namespaces the keys, so one Redis can serve several
	// environments. It should end with a separator, e.g. "harness:conv:".
	KeyPrefix string
}

// ConversationStore is an outbound.ConversationStore on Redis lists.
type ConversationStore struct {
	rdb  redis.Cmdable
	opts ConversationOptions
}

// NewConversationStore returns a store over rdb, or an error if opts are
// invalid.
func NewConversationStore(rdb redis.Cmdable, opts ConversationOptions) (*ConversationStore, error) {
	if opts.MaxMessagesPerConversation < 1 {
		return nil, fmt.Errorf("redisstore: MaxMessagesPerConversation must be >= 1, got %d", opts.MaxMessagesPerConversation)
	}
	if opts.TTL <= 0 {
		return nil, fmt.Errorf("redisstore: TTL must be positive, got %s", opts.TTL)
	}
	return &ConversationStore{rdb: rdb, opts: opts}, nil
}

func (s *ConversationStore) key(id string) string { return s.opts.KeyPrefix + id }

// stored is the wire form of a message. It is explicit rather than the domain
// struct so a change to conversation.Message cannot silently change what is
// already sitting in Redis.
type stored struct {
	Role    string `json:"r"`
	Content string `json:"c"`
}

// History implements outbound.ConversationStore.
func (s *ConversationStore) History(ctx context.Context, conversationID string) ([]conversation.Message, error) {
	raw, err := s.rdb.LRange(ctx, s.key(conversationID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("redisstore: read history: %w", err)
	}
	out := make([]conversation.Message, 0, len(raw))
	for _, item := range raw {
		var m stored
		if err := json.Unmarshal([]byte(item), &m); err != nil {
			return nil, fmt.Errorf("redisstore: decode message: %w", err)
		}
		out = append(out, conversation.Message{Role: conversation.Role(m.Role), Content: m.Content})
	}
	return out, nil
}

// Append implements outbound.ConversationStore.
func (s *ConversationStore) Append(ctx context.Context, conversationID string, messages ...conversation.Message) error {
	if len(messages) == 0 {
		return nil
	}
	values := make([]any, len(messages))
	for i, m := range messages {
		b, err := json.Marshal(stored{Role: string(m.Role), Content: m.Content})
		if err != nil {
			return fmt.Errorf("redisstore: encode message: %w", err)
		}
		values[i] = b
	}

	key := s.key(conversationID)
	// One transaction, so a reader never sees the list grown past its bound
	// and the TTL is never left unset.
	_, err := s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.RPush(ctx, key, values...)
		p.LTrim(ctx, key, int64(-s.opts.MaxMessagesPerConversation), -1)
		p.Expire(ctx, key, s.opts.TTL)
		return nil
	})
	if err != nil {
		return fmt.Errorf("redisstore: append: %w", err)
	}
	return nil
}
