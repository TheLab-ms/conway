package payment

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/settings"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/stripe/stripe-go/v78"
	billingsession "github.com/stripe/stripe-go/v78/billingportal/session"
	"github.com/stripe/stripe-go/v78/checkout/session"
	"github.com/stripe/stripe-go/v78/coupon"
	"github.com/stripe/stripe-go/v78/customer"
	"github.com/stripe/stripe-go/v78/price"
	"github.com/stripe/stripe-go/v78/subscription"
	"github.com/stripe/stripe-go/v78/webhook"
)

type Module struct {
	db         *sql.DB
	settings   *settings.Store
	self       *url.URL
	webhookKey atomic.Pointer[string]
}

func New(db *sql.DB, settingsStore *settings.Store, self *url.URL) *Module {
	settingsStore.RegisterSection(settings.Section{
		Title: "Stripe",
		Fields: []settings.Field{
			{Key: "stripe.key", Label: "API Key", Description: "Stripe API key", Sensitive: true},
			{Key: "stripe.webhook_key", Label: "Webhook Secret", Description: "Stripe webhook signing secret", Sensitive: true},
		},
	})

	return &Module{db: db, settings: settingsStore, self: self}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	ctx := context.Background()

	// Watch for webhook key changes
	m.settings.Watch(ctx, "stripe.webhook_key", func(v string) {
		if v != "" {
			m.webhookKey.Store(&v)
			slog.Info("stripe webhook key configured")
		} else {
			m.webhookKey.Store(nil)
		}
	})

	router.HandleFunc("POST /webhooks/stripe", m.handleStripeWebhook)
	router.HandleFunc("GET /payment/checkout", router.WithAuthn(m.handleCheckoutForm))
}

func (m *Module) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	webhookKey := m.webhookKey.Load()
	if webhookKey == nil || *webhookKey == "" {
		http.Error(w, "Stripe webhook not configured", http.StatusServiceUnavailable)
		return
	}

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Verify the signature of the request and parse it
	event, err := webhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), *webhookKey)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Filter out events we don't care about
	switch event.Type {
	case "customer.subscription.deleted":
	case "customer.subscription.updated":
	case "customer.subscription.created":
	default:
		slog.Debug("unhandled stripe webhook event", "type", event.Type)
		w.WriteHeader(204)
		return
	}

	// Get the latest state of the customer and subscription Stripe API objects
	subID := event.Data.Object["id"].(string)
	sub, err := subscription.Get(subID, &stripe.SubscriptionParams{})
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	cust, err := customer.Get(sub.Customer.ID, &stripe.CustomerParams{})
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Update our representation of the member to reflect Stripe
	_, err = m.db.ExecContext(r.Context(), "UPDATE members SET stripe_customer_id = $2, stripe_subscription_id = $3, stripe_subscription_state = $4, name = $5 WHERE email = $1", strings.ToLower(cust.Email), cust.ID, sub.ID, sub.Status, cust.Name)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	slog.Info("updated member's stripe subscription metadata", "member", cust.Email, "status", sub.Status)
	w.WriteHeader(204)
}

// handleCheckoutForm redirects users to the appropriate Stripe Checkout workflow.
func (m *Module) handleCheckoutForm(w http.ResponseWriter, r *http.Request) {
	// Check if Stripe is configured
	if stripe.Key == "" {
		http.Error(w, "Stripe is not configured", http.StatusServiceUnavailable)
		return
	}

	var email string
	var discountType *string
	var existingCustomerID *string
	var existingSubID *string
	var active bool
	var annual bool
	err := m.db.QueryRowContext(r.Context(), "SELECT email, discount_type, stripe_customer_id, stripe_subscription_id, bill_annually, (stripe_subscription_state IS NOT NULL AND stripe_subscription_state != 'canceled') FROM members WHERE id = ?", auth.GetUserMeta(r.Context()).ID).Scan(&email, &discountType, &existingCustomerID, &existingSubID, &annual, &active)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Allow existing subscriptions to be modified
	if active {
		sessionParams := &stripe.BillingPortalSessionParams{
			Customer:  existingCustomerID,
			ReturnURL: stripe.String(m.self.String()),
		}
		sessionParams.Context = r.Context()

		s, err := billingsession.New(sessionParams)
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}

		http.Redirect(w, r, s.URL, http.StatusSeeOther)
		return
	}

	// Create a new checkout session
	checkoutParams := &stripe.CheckoutSessionParams{
		Mode:       stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		SuccessURL: stripe.String(m.self.String()),
		CancelURL:  stripe.String(m.self.String()),
		LineItems:  []*stripe.CheckoutSessionLineItemParams{{Quantity: stripe.Int64(1)}},
	}
	checkoutParams.Context = r.Context()

	// Set the requested payment frequency
	freq := "monthly"
	if annual {
		freq = "yearly"
	}
	pricesIter := price.Search(&stripe.PriceSearchParams{
		SearchParams: stripe.SearchParams{
			Context: r.Context(),
			Limit:   stripe.Int64(1),
			Query:   fmt.Sprintf("active:'true' AND lookup_key:'%s'", freq),
		},
	})
	if !pricesIter.Next() {
		engine.SystemError(w, "price was not found in Stripe")
		return
	}
	price := pricesIter.Price()
	checkoutParams.LineItems[0].Price = &price.ID

	// Apply discount(s)
	if discountType != nil {
		coupIter := coupon.List(&stripe.CouponListParams{})

		for coupIter.Next() {
			coup := coupIter.Coupon()
			if coup.Metadata == nil || !coup.Valid || !slices.Contains(strings.Split(strings.ToLower(coup.Metadata["discountTypes"]), ","), strings.ToLower(*discountType)) {
				continue
			}
			checkoutParams.Discounts = []*stripe.CheckoutSessionDiscountParams{{
				Coupon: &coupIter.Coupon().ID,
			}}
			break
		}
	}

	// The member will already have a Stripe customer ID if they've had an active subscription previously.
	// We should pass it so Stripe won't create a duplicate customer object.
	if existingCustomerID == nil {
		checkoutParams.CustomerEmail = &email
	} else {
		checkoutParams.Customer = existingCustomerID
	}

	s, err := session.New(checkoutParams)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.Redirect(w, r, s.URL, http.StatusSeeOther)
}
