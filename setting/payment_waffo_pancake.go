package setting

// Waffo Pancake hosted checkout configuration. Gateway is enabled once
// MerchantID + PrivateKey + ProductID are populated (no separate Enabled
// flag, matching Stripe / Creem). StoreID + ProductID are operator-bound
// via SaveWaffoPancakeConfig.
var (
	WaffoPancakeMerchantID string
	WaffoPancakePrivateKey string
	WaffoPancakeReturnURL  string
	WaffoPancakeUnitPrice  float64 = 1.0
	WaffoPancakeMinTopUp   int     = 1
	WaffoPancakeStoreID    string
	WaffoPancakeProductID  string
	WaffoPancakeCurrency   string
	// WaffoPancakeExchangeRate is the CNY-per-USD rate used to convert the
	// CNY-denominated site price into the USD amount charged at checkout.
	// Setting it below the market rate charges proportionally more USD so
	// USD->CNY withdrawal fees and FX spread do not eat the margin.
	// 0 (default) follows the global USDExchangeRate.
	WaffoPancakeExchangeRate float64 = 0
)
