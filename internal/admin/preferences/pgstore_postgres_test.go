//go:build integration

package preferences

import (
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
)

func TestPgStoreMeetsTheContract(t *testing.T) {
	runStoreContract(t, func(t *testing.T) Store { return NewPgStore(testkit.NewPostgres(t)) })
}
