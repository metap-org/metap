package eventbus

import "context"

type messageIDKey struct{}

// WithMessageID makes Publish stamp the event with id as its message id. The outbox publisher passes the
// outbox row's id, so a republish of the same row (a commit that failed after the broker accepted the
// batch) carries the identity the first one did, and consumers can tell the copies apart from new events.
func WithMessageID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, messageIDKey{}, id)
}

func messageIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(messageIDKey{}).(string)
	return id
}
