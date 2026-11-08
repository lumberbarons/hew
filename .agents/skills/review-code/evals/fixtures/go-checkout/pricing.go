package checkout

// DiscountTier maps a minimum quantity to the fraction taken off the line total.
type DiscountTier struct {
	MinQty   int
	Fraction float64
}

// Tiers are ordered from largest qualifying quantity to smallest.
var Tiers = []DiscountTier{
	{MinQty: 100, Fraction: 0.15},
	{MinQty: 50, Fraction: 0.10},
	{MinQty: 10, Fraction: 0.05},
}

// VolumeDiscountFraction returns the discount fraction earned by qty.
func VolumeDiscountFraction(qty int) float64 {
	for _, t := range Tiers {
		if qty >= t.MinQty {
			return t.Fraction
		}
	}
	return 0
}

// LineTotalCents returns the discounted total for qty units at unitCents each.
// The discount rounds half up, matching TaxCents, so a total assembled from both
// uses one rounding convention throughout.
func LineTotalCents(unitCents int64, qty int) int64 {
	gross := unitCents * int64(qty)
	discount := int64(float64(gross)*VolumeDiscountFraction(qty) + 0.5)
	return gross - discount
}

// TaxCents returns the tax owed on subtotalCents at the given rate, rounded half up.
func TaxCents(subtotalCents int64, rate float64) int64 {
	return int64(float64(subtotalCents)*rate + 0.5)
}
