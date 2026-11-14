package crud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/permission"
)

// ActionCall is one invocation of an action declared in EntityDefinition.Actions.
type ActionCall struct {
	Entity string
	Name   string
	// ID is the record an action with Scope "record" acts on; uuid.Nil for Scope "entity".
	ID    uuid.UUID
	Input JsonObject
	RC    *permission.RequestContext
}

// ActionHandler runs one action. It must read and write through a RecordBackend with call.RC, so
// the caller's own permissions bound what it can do. A business failure is a *ServiceError; any
// other error is internal.
type ActionHandler func(ctx context.Context, call ActionCall) (JsonObject, error)

// ActionInvoker is what the gRPC ActionService and any in-process caller use.
type ActionInvoker interface {
	Invoke(ctx context.Context, call ActionCall) (JsonObject, error)
}

// ActionRegistry maps (entity, action name) to its handler. The zero value is not usable; use
// NewActionRegistry.
type ActionRegistry struct {
	mu       sync.RWMutex
	handlers map[string]ActionHandler
}

func NewActionRegistry() *ActionRegistry {
	return &ActionRegistry{handlers: map[string]ActionHandler{}}
}

func actionKey(entity, name string) string { return entity + "\x00" + name }

// Register adds a handler; registering the same (entity, name) twice is a programming error.
func (r *ActionRegistry) Register(entity, name string, h ActionHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := actionKey(entity, name)
	if _, dup := r.handlers[k]; dup {
		return fmt.Errorf("action %s.%s registered twice", entity, name)
	}
	r.handlers[k] = h
	return nil
}

// Invoke implements ActionInvoker. An unknown action is a 404.
func (r *ActionRegistry) Invoke(ctx context.Context, call ActionCall) (JsonObject, error) {
	r.mu.RLock()
	h, ok := r.handlers[actionKey(call.Entity, call.Name)]
	r.mu.RUnlock()
	if !ok {
		return nil, NewServiceErrorMsg(http.StatusNotFound, "action_not_found", fmt.Sprintf("No action %q on entity %q.", call.Name, call.Entity))
	}
	return h(ctx, call)
}

// Has reports whether a handler is registered.
func (r *ActionRegistry) Has(entity, name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.handlers[actionKey(entity, name)]
	return ok
}

// ToObject converts a handler's natural result (a map or struct) into the JSON object an action
// returns.
func ToObject(v any, err error) (JsonObject, error) {
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var obj JsonObject
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}
