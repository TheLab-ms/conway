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

	"github.com/TheLab-ms/conway/engine"
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

const migration = ``

// stripeConfig holds Stripe-related configuration.
type stripeConfig struct {
	apiKey     string
	webhookKey string
}

type Module struct {
	db          *sql.DB
	self        *url.URL
	eventLogger *engine.EventLogger
}

func New(db *sql.DB, self *url.URL, eventLogger *engine.EventLogger) *Module {
	return &Module{db: db, self: self, eventLogger: eventLogger}
}

// loadConfig loads Stripe configuration from the database.
func (m *Module) loadConfig(ctx context.Context) (*stripeConfig, error) {
	row := m.db.QueryRowContext(ctx,
		`SELECT api_key, webhook_key FROM stripe_config ORDER BY version DESC LIMIT 1`)

	cfg := &stripeConfig{}
	err := row.Scan(&cfg.apiKey, &cfg.webhookKey)
	if err == sql.ErrNoRows {
		return &stripeConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading stripe config: %w", err)
	}
	return cfg, nil
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("POST /webhooks/stripe", m.handleStripeWebhook)
	router.HandleFunc("GET /payment/checkout", router.WithAuthn(m.handleCheckoutForm))
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	// No workers needed - events are logged to unified integration_events table
}

func (m *Module) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	cfg, err := m.loadConfig(r.Context())
	if err != nil {
		m.eventLogger.LogEvent(r.Context(), "stripe", 0, "WebhookError", "", "", false, "config load: "+err.Error())
		engine.SystemError(w, err.Error())
		return
	}

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		m.eventLogger.LogEvent(r.Context(), "stripe", 0, "WebhookError", "", "", false, "body read: "+err.Error())
		engine.SystemError(w, err.Error())
		return
	}

	// Verify the signature of the request and parse it
	event, err := webhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), cfg.webhookKey)
	if err != nil {
		m.eventLogger.LogEvent(r.Context(), "stripe", 0, "WebhookError", "", "", false, "signature verification: "+err.Error())
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

	// Set API key from config if available
	if cfg.apiKey != "" {
		stripe.Key = cfg.apiKey
	}

	// Get the latest state of the customer and subscription Stripe API objects
	subID := event.Data.Object["id"].(string)
	sub, err := subscription.Get(subID, &stripe.SubscriptionParams{})
	if err != nil {
		m.eventLogger.LogEvent(r.Context(), "stripe", 0, "APIError", "", "", false, "subscription.Get: "+err.Error())
		engine.SystemError(w, err.Error())
		return
	}
	cust, err := customer.Get(sub.Customer.ID, &stripe.CustomerParams{})
	if err != nil {
		m.eventLogger.LogEvent(r.Context(), "stripe", 0, "APIError", sub.Customer.ID, "", false, "customer.Get: "+err.Error())
		engine.SystemError(w, err.Error())
		return
	}

	// Look up member ID for logging
	var memberID int64
	m.db.QueryRowContext(r.Context(), "SELECT id FROM members WHERE email = ?", strings.ToLower(cust.Email)).Scan(&memberID)

	// Update our representation of the member to reflect Stripe
	_, err = m.db.ExecContext(r.Context(), "UPDATE members SET stripe_customer_id = $2, stripe_subscription_id = $3, stripe_subscription_state = $4, name = $5 WHERE email = $1", strings.ToLower(cust.Email), cust.ID, sub.ID, sub.Status, cust.Name)
	if err != nil {
		m.eventLogger.LogEvent(r.Context(), "stripe", memberID, "WebhookError", cust.ID, "", false, "db update: "+err.Error())
		engine.SystemError(w, err.Error())
		return
	}

	m.eventLogger.LogEvent(r.Context(), "stripe", memberID, "WebhookReceived", cust.ID, "", true, fmt.Sprintf("event=%s status=%s", event.Type, sub.Status))
	slog.Info("updated member's stripe subscription metadata", "member", cust.Email, "status", sub.Status)
	w.WriteHeader(204)
}

// handleCheckoutForm redirects users to the appropriate Stripe Checkout workflow.
func (m *Module) handleCheckoutForm(w http.ResponseWriter, r *http.Request) {
	cfg, err := m.loadConfig(r.Context())
	if err != nil {
		m.eventLogger.LogEvent(r.Context(), "stripe", auth.GetUserMeta(r.Context()).ID, "APIError", "", "", false, "config load: "+err.Error())
		engine.SystemError(w, err.Error())
		return
	}

	// Set API key from config
	if cfg.apiKey != "" {
		stripe.Key = cfg.apiKey
	}

	memberID := auth.GetUserMeta(r.Context()).ID
	var email string
	var discountType *string
	var existingCustomerID *string
	var existingSubID *string
	var active bool
	var annual bool
	err = m.db.QueryRowContext(r.Context(), "SELECT email, discount_type, stripe_customer_id, stripe_subscription_id, bill_annually, (stripe_subscription_state IS NOT NULL AND stripe_subscription_state != 'canceled') FROM members WHERE id = ?", memberID).Scan(&email, &discountType, &existingCustomerID, &existingSubID, &annual, &active)
	if err != nil {
		m.eventLogger.LogEvent(r.Context(), "stripe", memberID, "APIError", "", "", false, "db query: "+err.Error())
		engine.SystemError(w, err.Error())
		return
	}

	custID := ""
	if existingCustomerID != nil {
		custID = *existingCustomerID
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
			m.eventLogger.LogEvent(r.Context(), "stripe", memberID, "APIError", custID, "", false, "billingportal.session.New: "+err.Error())
			engine.SystemError(w, err.Error())
			return
		}

		m.eventLogger.LogEvent(r.Context(), "stripe", memberID, "BillingPortal", custID, "", true, "redirected to billing portal")
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
		m.eventLogger.LogEvent(r.Context(), "stripe", memberID, "APIError", custID, "", false, "price.Search: price not found for "+freq)
		engine.SystemError(w, "price was not found in Stripe")
		return
	}
	priceObj := pricesIter.Price()
	checkoutParams.LineItems[0].Price = &priceObj.ID

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
		m.eventLogger.LogEvent(r.Context(), "stripe", memberID, "APIError", custID, "", false, "checkout.session.New: "+err.Error())
		engine.SystemError(w, err.Error())
		return
	}

	m.eventLogger.LogEvent(r.Context(), "stripe", memberID, "CheckoutCreated", custID, "", true, fmt.Sprintf("freq=%s", freq))
	http.Redirect(w, r, s.URL, http.StatusSeeOther)
}
