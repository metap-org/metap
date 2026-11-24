package rolestore

import (
	"sync"

	"github.com/google/uuid"
)

// Change says what a write in this package changed, for whatever keeps a copy of it in memory (the
// role cache of the HTTP layer, the policy matrix): so that on the instance that made the change it
// shows at once, instead of after the cache's time to live (roadmap 120).
type Change struct {
	Tenant uuid.UUID
	// User is the member whose roles changed; uuid.Nil when it is not one member (a scope went away).
	User uuid.UUID
	// Policies is true when the tenant's policies changed.
	Policies bool
}

var (
	listenersMu  sync.RWMutex
	listeners    = map[int]func(Change){}
	nextListener int
)

// Subscribe registers fn to be told of every Change made through this package in this process, and
// returns the function that removes it. A change made by another process is not seen: caches keep
// their time to live for that.
//
// fn runs when the write is issued, which inside a transaction is before it commits; a request that
// re-reads in that gap can cache the old value until the time to live runs out. That is the
// accepted bound, not a gap this closes.
func Subscribe(fn func(Change)) (unsubscribe func()) {
	listenersMu.Lock()
	defer listenersMu.Unlock()
	id := nextListener
	nextListener++
	listeners[id] = fn
	return func() {
		listenersMu.Lock()
		delete(listeners, id)
		listenersMu.Unlock()
	}
}

func notify(c Change) {
	listenersMu.RLock()
	fns := make([]func(Change), 0, len(listeners))
	for _, fn := range listeners {
		fns = append(fns, fn)
	}
	listenersMu.RUnlock()
	for _, fn := range fns {
		fn(c)
	}
}
