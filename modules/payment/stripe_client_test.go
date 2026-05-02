package payment

import (
	"sync"
	"testing"

	"github.com/stripe/stripe-go/v78"
)

// TestStripeClient_NoGlobalMutation asserts that constructing a per-call
// stripeClient never mutates the package-global stripe.Key. This is a
// regression test for the multi-tenant safety property described in the
// refactoring report (#4): handlers must use scoped clients so concurrent
// calls with different keys do not race on a shared global.
func TestStripeClient_NoGlobalMutation(t *testing.T) {
	original := stripe.Key
	t.Cleanup(func() { stripe.Key = original })

	stripe.Key = "sk_test_sentinel_should_not_change"

	c1 := stripeClient("sk_test_alpha")
	c2 := stripeClient("sk_test_beta")

	if stripe.Key != "sk_test_sentinel_should_not_change" {
		t.Fatalf("stripe.Key was mutated to %q; per-call clients must not touch the global", stripe.Key)
	}

	// Each client should also carry its own key on the embedded service clients.
	if c1.Customers.Key != "sk_test_alpha" {
		t.Fatalf("client 1 has key %q, want sk_test_alpha", c1.Customers.Key)
	}
	if c2.Customers.Key != "sk_test_beta" {
		t.Fatalf("client 2 has key %q, want sk_test_beta", c2.Customers.Key)
	}
}

// TestStripeClient_ConcurrentScopedKeys ensures two goroutines using
// different per-call clients do not see each other's API key. This would
// have failed under the old implementation that wrote stripe.Key from
// each handler.
func TestStripeClient_ConcurrentScopedKeys(t *testing.T) {
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(2)

	errs := make(chan string, 2*iterations)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			c := stripeClient("sk_test_alpha")
			if c.Customers.Key != "sk_test_alpha" {
				errs <- "alpha goroutine saw key=" + c.Customers.Key
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			c := stripeClient("sk_test_beta")
			if c.Customers.Key != "sk_test_beta" {
				errs <- "beta goroutine saw key=" + c.Customers.Key
			}
		}
	}()

	wg.Wait()
	close(errs)

	for e := range errs {
		t.Error(e)
	}
}
