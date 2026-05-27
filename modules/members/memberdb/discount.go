package memberdb

// DiscountOption describes a single value the members.discount_type column
// is allowed to take, paired with the human-readable label used in UIs.
//
// The empty Value represents "no discount" (stored as NULL in the column).
type DiscountOption struct {
	Value string
	Label string
}

// DiscountTypes is the canonical, ordered list of valid discount_type values
// used everywhere a UI needs to render a picker (admin form, Discord bot
// signup message, etc.).
//
// Keep this in sync with the no_discount_after_cancelation trigger and the
// Stripe coupon metadata.discountTypes mapping documented in
// modules/payment/README.md.
var DiscountTypes = []DiscountOption{
	{Value: "", Label: "None"},
	{Value: "military", Label: "Military"},
	{Value: "retired", Label: "Retired"},
	{Value: "unemployed", Label: "Unemployed"},
	{Value: "firstResponder", Label: "First Responder"},
	{Value: "student", Label: "Student"},
	{Value: "educator", Label: "Educator"},
	{Value: "emeritus", Label: "Emeritus"},
	{Value: "family", Label: "Family"},
}

// IsValidDiscountType reports whether v is one of the allowed discount_type
// values (including the empty "no discount" sentinel).
func IsValidDiscountType(v string) bool {
	for _, opt := range DiscountTypes {
		if opt.Value == v {
			return true
		}
	}
	return false
}

// DiscountLabel returns the display label for the given discount value, or
// the value itself if it isn't a known option.
func DiscountLabel(v string) string {
	for _, opt := range DiscountTypes {
		if opt.Value == v {
			return opt.Label
		}
	}
	return v
}
