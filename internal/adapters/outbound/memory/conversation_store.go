// Package memory implements outbound.ConversationStore in process memory.
//
// It keeps the harness dependency-free to run, and every dimension of what
// it holds is bounded so it cannot grow without limit:
//   - at most MaxConversations conversations (least recently active evicted);
//   - at most MaxMessagesPerConversation messages each (oldest dropped);
//   - conversations idle longer than TTL are treated as gone.
//
// It is NOT suitable for more than one running instance: each instance
// would see only the conversations it happened to serve. Scaling out means
// replacing this adapter with one backed by shared storage behind the same
// outbound.ConversationStore port.
package memory

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/conversation"
)

// Options bounds what a ConversationStore retains. Every field is required.
type Options struct {
	MaxConversations           int
	MaxMessagesPerConversation int
	TTL                        time.Duration

	// Now returns the current time. Nil means time.Now; tests override it.
	Now func() time.Time
}

// ConversationStore is a thread-safe, bounded, in-memory
// outbound.ConversationStore.
type ConversationStore struct {
	opts Options

	mu    sync.Mutex
	byID  map[string]*list.Element
	order *list.List // front = most recently active
}

type entry struct {
	id         string
	messages   []conversation.Message
	lastActive time.Time
}

// New returns an empty ConversationStore, or an error if opts are invalid.
func New(opts Options) (*ConversationStore, error) {
	if opts.MaxConversations < 1 {
		return nil, fmt.Errorf("memory: MaxConversations must be >= 1, got %d", opts.MaxConversations)
	}
	if opts.MaxMessagesPerConversation < 1 {
		return nil, fmt.Errorf("memory: MaxMessagesPerConversation must be >= 1, got %d", opts.MaxMessagesPerConversation)
	}
	if opts.TTL <= 0 {
		return nil, fmt.Errorf("memory: TTL must be positive, got %s", opts.TTL)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &ConversationStore{
		opts:  opts,
		byID:  make(map[string]*list.Element),
		order: list.New(),
	}, nil
}

// History returns a copy of the retained messages for conversationID, or an
// empty slice if it is unknown or has expired.
func (s *ConversationStore) History(_ context.Context, conversationID string) ([]conversation.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	elem, ok := s.byID[conversationID]
	if !ok {
		return []conversation.Message{}, nil
	}
	e := elem.Value.(*entry)
	if s.expired(e) {
		s.removeElement(elem)
		return []conversation.Message{}, nil
	}

	out := make([]conversation.Message, len(e.messages))
	copy(out, e.messages)
	return out, nil
}

// Append adds messages to conversationID, creating it if needed and
// enforcing every bound in Options.
func (s *ConversationStore) Append(_ context.Context, conversationID string, messages ...conversation.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.opts.Now()
	s.evictExpired()

	elem, ok := s.byID[conversationID]
	if !ok {
		for s.order.Len() >= s.opts.MaxConversations {
			s.removeElement(s.order.Back())
		}
		elem = s.order.PushFront(&entry{id: conversationID})
		s.byID[conversationID] = elem
	}

	e := elem.Value.(*entry)
	e.messages = append(e.messages, messages...)
	if excess := len(e.messages) - s.opts.MaxMessagesPerConversation; excess > 0 {
		// Copy into a fresh slice so the dropped prefix can be garbage
		// collected instead of pinning the old backing array forever.
		e.messages = append([]conversation.Message(nil), e.messages[excess:]...)
	}
	e.lastActive = now
	s.order.MoveToFront(elem)
	return nil
}

// Len reports how many conversations are currently retained, expired ones
// included until they are next swept.
func (s *ConversationStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.order.Len()
}

// evictExpired drops expired conversations from the least-recently-active
// end. Because the list is ordered by activity, it can stop at the first
// live entry. Caller must hold s.mu.
func (s *ConversationStore) evictExpired() {
	for elem := s.order.Back(); elem != nil; elem = s.order.Back() {
		if !s.expired(elem.Value.(*entry)) {
			return
		}
		s.removeElement(elem)
	}
}

func (s *ConversationStore) expired(e *entry) bool {
	return s.opts.Now().Sub(e.lastActive) > s.opts.TTL
}

func (s *ConversationStore) removeElement(elem *list.Element) {
	s.order.Remove(elem)
	delete(s.byID, elem.Value.(*entry).id)
}
