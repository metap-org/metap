//go:build integration

package identity_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/testkit"
)

// guessInParallel runs n wrong guesses at once and counts the errors they got.
func guessInParallel(n int, guess func() error) map[error]int {
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[error]int{}
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := guess()
			mu.Lock()
			defer mu.Unlock()
			for _, known := range []error{identity.ErrInvalidCredentials, identity.ErrLocked} {
				if errors.Is(err, known) {
					err = known
				}
			}
			got[err]++
		}()
	}
	close(start)
	wg.Wait()
	return got
}

func lockoutMember(t *testing.T) (context.Context, identity.DBTX, *identity.Member) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	m, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: uuid.New(), Email: "lock@example.com", Password: "right-password-1"})
	require.NoError(t, err)
	return ctx, pool, m
}

// Guesses sent together are limited like guesses sent one after another: only MaxFailedLogins of
// them have their password judged.
func TestParallelWrongGuessesCannotOutrunTheLockout(t *testing.T) {
	ctx, db, m := lockoutMember(t)
	now := time.Now()
	got := guessInParallel(40, func() error {
		return identity.VerifyCurrentPassword(ctx, db, m.IdentityID, "wrong-guess", now)
	})
	assert.Equal(t, map[error]int{identity.ErrInvalidCredentials: identity.MaxFailedLogins, identity.ErrLocked: 40 - identity.MaxFailedLogins}, got)
	assert.ErrorIs(t, identity.VerifyCurrentPassword(ctx, db, m.IdentityID, "right-password-1", now), identity.ErrLocked)
}

func TestParallelSignInGuessesLockTheAccount(t *testing.T) {
	ctx, db, _ := lockoutMember(t)
	now := time.Now()
	guessInParallel(40, func() error {
		_, err := identity.Authenticate(ctx, db, "lock@example.com", "wrong-guess", now)
		return err
	})
	_, err := identity.Authenticate(ctx, db, "lock@example.com", "right-password-1", now)
	assert.Error(t, err, "the account is locked")
}

// While an account is locked, the right password is answered exactly like a wrong one: otherwise
// the lock still tells a guesser which password is right.
func TestALockedSignInDoesNotTellTheRightPasswordApart(t *testing.T) {
	ctx, db, _ := lockoutMember(t)
	now := time.Now()
	sign := func(password string, at time.Time) error {
		_, err := identity.Authenticate(ctx, db, "lock@example.com", password, at)
		return err
	}
	for range identity.MaxFailedLogins {
		assert.ErrorIs(t, sign("wrong-guess", now), identity.ErrInvalidCredentials)
	}
	right, wrong := sign("right-password-1", now), sign("wrong-guess", now)
	assert.Equal(t, wrong, right)
	assert.ErrorIs(t, right, identity.ErrInvalidCredentials)

	// Guessing during the lock does not extend it.
	assert.NoError(t, sign("right-password-1", now.Add(identity.LockoutDuration+time.Second)))
}

// The attempt that reaches the limit still succeeds when its password is right.
func TestTheLastAllowedAttemptCanStillSignIn(t *testing.T) {
	ctx, db, _ := lockoutMember(t)
	now := time.Now()
	for range identity.MaxFailedLogins - 1 {
		_, err := identity.Authenticate(ctx, db, "lock@example.com", "wrong-guess", now)
		assert.ErrorIs(t, err, identity.ErrInvalidCredentials)
	}
	_, err := identity.Authenticate(ctx, db, "lock@example.com", "right-password-1", now)
	require.NoError(t, err)
	_, err = identity.Authenticate(ctx, db, "lock@example.com", "right-password-1", now)
	require.NoError(t, err, "the successful attempt cleared the count and the lock")
}
