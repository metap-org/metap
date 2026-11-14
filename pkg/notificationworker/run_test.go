package notificationworker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/events"
	"github.com/metap-org/metap/pkg/infra/eventbus"
)

// Run's handler decodes through the shared payload type; a payload it cannot read must not
// panic the consumer.
func TestNotifyToleratesUnreadablePayload(t *testing.T) {
	require.NotPanics(t, func() {
		notify(&eventbus.ConsumedEvent{RoutingKey: "a.b.workflow.transitioned", Payload: []byte(`[1]`)})
	})
}

func TestBindingIsTheSharedTransitionPattern(t *testing.T) {
	require.Equal(t, events.AnyTransition, RoutingKey)
}
