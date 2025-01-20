package payment

// TODO
// func TestCheckoutRedirectIntegration(t *testing.T) {
// 	key := os.Getenv("STRIPE_INTEGRATION_KEY")
// 	if key == "" {
// 		t.Skipf("skipping because STRIPE_INTEGRATION_KEY isn't set")
// 	}
// 	stripe.Key = key

// 	db := db.NewTest(t)
// 	m := New(db, "", &url.URL{Scheme: "https", Host: "anything.ms"})

// 	// Just prove that the basic configurations can successfully start a checkout session
// 	urls := []string{"/?freq=monthly", "/?freq=yearly", "/?freq=monthly&discount=foobar", "/?freq=yearly&discount=foobar"}
// 	for _, u := range urls {
// 		t.Run(u, func(t *testing.T) {
// 			w := httptest.NewRecorder()
// 			r := httptest.NewRequest("POST", u, nil)
// 			engine.ExecHandler(w, r, nil, m.initiateCheckout(r, "foo@bar.com", "", ""))

// 			assert.Equal(t, 303, w.Result().StatusCode)
// 			assert.Contains(t, w.Result().Header.Get("Location"), "checkout.stripe.com")
// 		})
// 	}
// }
