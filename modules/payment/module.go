package payment

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/julienschmidt/httprouter"
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
	webhookKey string
	self       *url.URL
}

func New(db *sql.DB, webhookKey string, self *url.URL) *Module {
	return &Module{db: db, webhookKey: webhookKey, self: self}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("POST", "/webhooks/stripe", m.handleStripeWebhook)
	router.Handle("GET", "/payment/checkout", router.WithAuth(m.handleCheckoutForm))
}

func (m *Module) handleStripeWebhook(r *http.Request, ps httprouter.Params) engine.Response {
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		return engine.Errorf("reading body: %s", err)
	}

	// Verify the signature of the request and parse it
	event, err := webhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), m.webhookKey)
	if err != nil {
		return engine.Errorf("parsing event: %s", err)
	}

	// Filter out events we don't care about
	switch event.Type {
	case "customer.subscription.deleted":
	case "customer.subscription.updated":
	case "customer.subscription.created":
	default:
		slog.Debug("unhandled stripe webhook event", "type", event.Type)
		return nil
	}

	// Get the latest state of the customer and subscription Stripe API objects
	subID := event.Data.Object["id"].(string)
	sub, err := subscription.Get(subID, &stripe.SubscriptionParams{})
	if err != nil {
		return engine.Errorf("getting current subscription: %s", err)
	}
	cust, err := customer.Get(sub.Customer.ID, &stripe.CustomerParams{})
	if err != nil {
		return engine.Errorf("getting current customer: %s", err)
	}

	// Update our representation of the member to reflect Stripe
	err = m.updateMemberStripeMetadata(r.Context(), cust, sub)
	if err != nil {
		return engine.Errorf("updating member metadata: %s", err)
	}

	slog.Info("updated member's stripe subscription metadata", "member", cust.Email, "status", sub.Status)
	return nil
}

func (m *Module) updateMemberStripeMetadata(ctx context.Context, cust *stripe.Customer, sub *stripe.Subscription) error {
	if sub.CanceledAt > 0 {
		_, err := m.db.ExecContext(ctx, "UPDATE members SET stripe_subscription_id = NULL, stripe_subscription_state = NULL WHERE email = $1", cust.Email)
		return err
	}

	_, err := m.db.ExecContext(ctx, "UPDATE members SET stripe_customer_id = $2, stripe_subscription_id = $3, stripe_subscription_state = $4, name = $5 WHERE email = $1", cust.Email, cust.ID, sub.ID, sub.Status, cust.Name)
	return err
}

// handleCheckoutForm redirects users to the appropriate Stripe Checkout workflow.
func (m *Module) handleCheckoutForm(r *http.Request, ps httprouter.Params) engine.Response {
	var email string
	var discountType *string
	var existingCustomerID *string
	var existingSubID *string
	err := m.db.QueryRowContext(r.Context(), "SELECT email, discount_type, stripe_customer_id, stripe_subscription_id FROM members WHERE id = ?", auth.GetUserMeta(r.Context()).ID).Scan(&email, &discountType, &existingCustomerID, &existingSubID)
	if err != nil {
		return engine.Errorf("querying db for member: %s", err)
	}

	return m.initiateCheckout(r, email, discountType, existingSubID, existingCustomerID)
}

func (m *Module) initiateCheckout(r *http.Request, email string, discountType, subID, custID *string) engine.Response {
	// Allow existing subscriptions to be modified
	if subID != nil {
		sessionParams := &stripe.BillingPortalSessionParams{
			Customer:  custID,
			ReturnURL: stripe.String(m.self.String()),
		}
		sessionParams.Context = r.Context()

		s, err := billingsession.New(sessionParams)
		if err != nil {
			return engine.Errorf("creating stripe billing session: %s", err)
		}

		return engine.Redirect(s.URL, http.StatusSeeOther)
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
	pricesIter := price.Search(&stripe.PriceSearchParams{
		SearchParams: stripe.SearchParams{
			Context: r.Context(),
			Limit:   stripe.Int64(1),
			Query:   "active:'true' AND lookup_key:'monthly'",
		},
	})
	if !pricesIter.Next() {
		return engine.Errorf("price was not found in Stripe")
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
	if custID == nil {
		checkoutParams.CustomerEmail = &email
	} else {
		checkoutParams.Customer = custID
	}

	s, err := session.New(checkoutParams)
	if err != nil {
		return engine.Errorf("creating stripe checkout session: %s", err)
	}

	return engine.Redirect(s.URL, http.StatusSeeOther)
}
