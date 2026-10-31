package eventbus

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// topicMatches reports whether routingKey matches pattern under AMQP topic-exchange rules:
// .-separated words, * matches exactly one word, # matches zero or more words.
func topicMatches(pattern, routingKey string) bool {
	return matchesSegments(strings.Split(pattern, "."), strings.Split(routingKey, "."))
}

func matchesSegments(pattern, key []string) bool {
	if len(pattern) == 0 {
		return len(key) == 0
	}
	head, rest := pattern[0], pattern[1:]
	switch head {
	case "#":
		if len(rest) == 0 {
			return true
		}
		for i := 0; i <= len(key); i++ {
			if matchesSegments(rest, key[i:]) {
				return true
			}
		}
		return false
	case "*":
		return len(key) > 0 && matchesSegments(rest, key[1:])
	default:
		return len(key) > 0 && key[0] == head && matchesSegments(rest, key[1:])
	}
}

type handlerFn func(ctx context.Context, event *ConsumedEvent) error

type registeredHandler struct {
	pattern string
	handler handlerFn
}

// HandlerRegistry lets one process register multiple pattern-matched handlers on a single
// subscription, instead of each needing its own queue/consumer.
type HandlerRegistry struct {
	handlers []registeredHandler
}

func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{}
}

// On registers handler to run for every event whose routing key matches pattern (AMQP wildcards
// */# allowed). Returns the registry for chaining, matching the Rust builder style.
func (r *HandlerRegistry) On(pattern string, handler handlerFn) *HandlerRegistry {
	r.handlers = append(r.handlers, registeredHandler{pattern: pattern, handler: handler})
	return r
}

// Run runs every registered handler against every event on queue until ctx is canceled. For
// each event, every handler whose pattern matches runs concurrently; the delivery acks only
// once all matching handlers return nil. Any handler returning an error fails the whole event —
// retryPolicy retries it with backoff, nil dead-letters it immediately.
func (r *HandlerRegistry) Run(ctx context.Context, queue string, retryPolicy *RetryPolicy, connect func() (EventBus, error)) error {
	handlers := r.handlers
	return RunResilientConsumer(ctx, queue, "#", retryPolicy, connect, func(ctx context.Context, event *ConsumedEvent) {
		var matched []handlerFn
		for _, h := range handlers {
			if topicMatches(h.pattern, event.RoutingKey) {
				matched = append(matched, h.handler)
			}
		}
		if len(matched) == 0 {
			_ = event.Ack()
			return
		}

		var wg sync.WaitGroup
		errs := make([]error, len(matched))
		for i, h := range matched {
			wg.Add(1)
			go func(i int, h handlerFn) {
				defer wg.Done()
				errs[i] = h(ctx, event)
			}(i, h)
		}
		wg.Wait()

		var failures []error
		for _, err := range errs {
			if err != nil {
				failures = append(failures, err)
			}
		}
		if len(failures) == 0 {
			_ = event.Ack()
			return
		}
		for _, err := range failures {
			slog.Warn("event handler failed", "routing_key", event.RoutingKey, "error", err)
		}
		if retryPolicy != nil {
			_ = event.RetryOrGiveUp(ctx, retryPolicy)
		} else {
			_ = event.Nack(false)
		}
	})
}
