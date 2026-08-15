package vault

import (
	"errors"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/clock/fakeclock"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/cenkalti/backoff"
)

type authenticationLifecycle string

const (
	authenticationUnavailable authenticationLifecycle = "authentication unavailable"
	sessionAuthenticated      authenticationLifecycle = "session authenticated"
	leaseRenewalUnavailable   authenticationLifecycle = "lease renewal unavailable"
	leaseRenewed              authenticationLifecycle = "lease renewed"
)

type authenticationAvailability struct {
	login   bool
	renewal bool
}

type authenticationBackend struct {
	mu           sync.RWMutex
	availability authenticationAvailability
	lease        time.Duration
	lifecycle    chan authenticationLifecycle
}

type authenticationClock struct {
	*fakeclock.FakeClock
	sleepScheduled chan time.Duration
}

func newAuthenticationClock(now time.Time) *authenticationClock {
	return &authenticationClock{
		FakeClock:      fakeclock.NewFakeClock(now),
		sleepScheduled: make(chan time.Duration, 1),
	}
}

func (authClock *authenticationClock) Sleep(delay time.Duration) {
	authClock.sleepScheduled <- delay
	authClock.FakeClock.Sleep(delay)
}

func newAuthenticationBackend(lease time.Duration, availability authenticationAvailability) *authenticationBackend {
	return &authenticationBackend{
		availability: availability,
		lease:        lease,
		lifecycle:    make(chan authenticationLifecycle, 16),
	}
}

func (backend *authenticationBackend) setLoginAvailable(available bool) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.availability.login = available
}

func (backend *authenticationBackend) setRenewalAvailable(available bool) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.availability.renewal = available
}

func (backend *authenticationBackend) Login() (time.Duration, error) {
	backend.mu.RLock()
	available := backend.availability.login
	lease := backend.lease
	backend.mu.RUnlock()

	if !available {
		backend.lifecycle <- authenticationUnavailable
		return 0, errors.New("vault authentication unavailable")
	}

	backend.lifecycle <- sessionAuthenticated
	return lease, nil
}

func (backend *authenticationBackend) Renew() (time.Duration, error) {
	backend.mu.RLock()
	available := backend.availability.renewal
	lease := backend.lease
	backend.mu.RUnlock()

	if !available {
		backend.lifecycle <- leaseRenewalUnavailable
		return 0, errors.New("vault lease renewal unavailable")
	}

	backend.lifecycle <- leaseRenewed
	return lease, nil
}

func awaitLifecycle(t *testing.T, backend *authenticationBackend, expected authenticationLifecycle) {
	t.Helper()

	select {
	case observed := <-backend.lifecycle:
		if observed != expected {
			t.Fatalf("expected lifecycle %q, got %q", expected, observed)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for lifecycle %q", expected)
	}
}

func expectNotLoggedIn(t *testing.T, loggedIn <-chan struct{}) {
	t.Helper()

	select {
	case <-loggedIn:
		t.Fatal("authenticated while the Vault backend was unavailable")
	default:
	}
}

func awaitLoggedIn(t *testing.T, loggedIn <-chan struct{}) {
	t.Helper()

	select {
	case <-loggedIn:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the authenticated session")
	}
}

func awaitScheduledSleep(t *testing.T, authClock *authenticationClock) time.Duration {
	t.Helper()

	select {
	case delay := <-authClock.sleepScheduled:
		return delay
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for authentication retry timer")
		return 0
	}
}

func randomizedIntervalBounds(interval time.Duration) (time.Duration, time.Duration) {
	delta := time.Duration(float64(interval) * backoff.DefaultRandomizationFactor)
	return interval - delta, interval + delta
}

func nextRetryInterval(interval, maxInterval time.Duration) time.Duration {
	if float64(interval) >= float64(maxInterval)/backoff.DefaultMultiplier {
		return maxInterval
	}
	return time.Duration(float64(interval) * backoff.DefaultMultiplier)
}

func assertScheduledRetryWithin(t *testing.T, delay, retryInterval time.Duration) {
	t.Helper()

	lower, upper := randomizedIntervalBounds(retryInterval)
	if delay < lower || delay > upper {
		t.Fatalf("retry delay %s outside randomized bounds [%s, %s] for interval %s", delay, lower, upper, retryInterval)
	}
}

func closeReAuther(reauth *ReAuther, authClock *fakeclock.FakeClock) {
	reauth.Close()
	authClock.WaitForWatcherAndIncrement(24 * time.Hour)
}

func TestReAutherTracksAuthenticationAndLeaseLifecycle(t *testing.T) {
	authClock := fakeclock.NewFakeClock(time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC))
	backend := newAuthenticationBackend(8*time.Minute, authenticationAvailability{login: true, renewal: true})
	reauth := newReAuther(
		lagertest.NewTestLogger("vault-test"),
		backend,
		10*time.Minute,
		time.Second,
		4*time.Second,
		authClock,
	)
	defer closeReAuther(reauth, authClock)

	awaitLifecycle(t, backend, sessionAuthenticated)
	awaitLoggedIn(t, reauth.LoggedIn())

	authClock.WaitForWatcherAndIncrement(4 * time.Minute)
	awaitLifecycle(t, backend, leaseRenewed)

	// The renewed lease extends beyond the configured maximum session TTL.
	// Moving to just before that boundary must keep the current session.
	authClock.WaitForWatcherAndIncrement(5 * time.Minute)
	select {
	case observed := <-backend.lifecycle:
		t.Fatalf("unexpected lifecycle before maximum TTL: %q", observed)
	default:
	}

	authClock.WaitForWatcherAndIncrement(time.Minute + time.Nanosecond)
	awaitLifecycle(t, backend, sessionAuthenticated)
}

func TestReAutherBoundsRetriesUntilAuthenticationAvailable(t *testing.T) {
	const (
		baseRetryInterval = time.Second
		maxRetryInterval  = 2 * time.Second
	)

	authClock := newAuthenticationClock(time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC))
	backend := newAuthenticationBackend(8*time.Minute, authenticationAvailability{renewal: true})
	reauth := newReAuther(
		lagertest.NewTestLogger("vault-test"),
		backend,
		0,
		baseRetryInterval,
		maxRetryInterval,
		authClock,
	)
	defer closeReAuther(reauth, authClock.FakeClock)

	for retryInterval := baseRetryInterval; retryInterval < maxRetryInterval; retryInterval = nextRetryInterval(retryInterval, maxRetryInterval) {
		awaitLifecycle(t, backend, authenticationUnavailable)
		expectNotLoggedIn(t, reauth.LoggedIn())

		delay := awaitScheduledSleep(t, authClock)
		assertScheduledRetryWithin(t, delay, retryInterval)
		authClock.WaitForWatcherAndIncrement(delay)
	}

	awaitLifecycle(t, backend, authenticationUnavailable)
	expectNotLoggedIn(t, reauth.LoggedIn())
	cappedDelay := awaitScheduledSleep(t, authClock)
	assertScheduledRetryWithin(t, cappedDelay, maxRetryInterval)

	backend.setLoginAvailable(true)
	authClock.WaitForWatcherAndIncrement(cappedDelay)

	awaitLifecycle(t, backend, sessionAuthenticated)
	awaitLoggedIn(t, reauth.LoggedIn())
}

func TestReAutherRecoversWhenLeaseRenewalBecomesAvailable(t *testing.T) {
	const maxRetryInterval = 4 * time.Second

	authClock := fakeclock.NewFakeClock(time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC))
	backend := newAuthenticationBackend(8*time.Minute, authenticationAvailability{login: true})
	reauth := newReAuther(
		lagertest.NewTestLogger("vault-test"),
		backend,
		0,
		time.Second,
		maxRetryInterval,
		authClock,
	)
	defer closeReAuther(reauth, authClock)

	awaitLifecycle(t, backend, sessionAuthenticated)
	authClock.WaitForWatcherAndIncrement(4 * time.Minute)
	awaitLifecycle(t, backend, leaseRenewalUnavailable)

	backend.setRenewalAvailable(true)
	authClock.WaitForWatcherAndIncrement(2 * maxRetryInterval)
	awaitLifecycle(t, backend, leaseRenewed)
}
