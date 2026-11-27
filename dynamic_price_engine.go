// Package task prices the goods of a non player character shop from stock and
// demand, keeps the price inside a band, restocks on a fixed cadence and guards
// the buy and sell spread against round trip arbitrage.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Side selects the direction of a quote.
type Side string

// Trade sides.
const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

// Errors reported by the price engine.
var (
	ErrBadItem          = errors.New("item id is required")
	ErrUnknownItem      = errors.New("unknown item")
	ErrBadBase          = errors.New("base price must be positive")
	ErrBadTarget        = errors.New("target stock must be positive")
	ErrBadBand          = errors.New("price band is invalid")
	ErrBadElasticity    = errors.New("elasticity is outside the supported range")
	ErrBadQty           = errors.New("quantity must be positive")
	ErrBadSide          = errors.New("unknown trade side")
	ErrBadClock         = errors.New("clock moved before the restock anchor")
	ErrInsufficientStock = errors.New("shop stock is too low")
	ErrInsufficientGold = errors.New("player balance is too low")
	ErrStockFull        = errors.New("shop stock would exceed the cap")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Band is the closed interval a price is clamped into.
type Band struct {
	Min int64
	Max int64
}

// PriceConfig is the shop policy.
type PriceConfig struct {
	Now               func() int64
	QuoteTTL          int64
	RestockPeriod     int64
	RestockQty        int
	SellMarginPercent int
	MaxStock          int
	MaxHistory        int
	DefaultBand       Band
}

// Quote is one offer computed from the current stock.
type Quote struct {
	Item      string
	Side      Side
	Qty       int
	UnitPrice int64
	Total     int64
	Stock     int
	Version   int64
	ExpiresAt int64
}

// TradeResult is the booked outcome of a buy or a sell.
type TradeResult struct {
	Player     string
	Item       string
	Side       Side
	Qty        int
	UnitPrice  int64
	Total      int64
	StockAfter int
	Balance    int64
	Repriced   bool
}

// ItemRestock is the restock line of one item.
type ItemRestock struct {
	Item    string
	Periods int
	Applied int
	Stock   int
	Anchor  int64
}

// RestockReport summarises one restock pass.
type RestockReport struct {
	At      int64
	Items   []ItemRestock
	Applied int
}

// PriceStats summarises one engine.
type PriceStats struct {
	Items    int
	Trades   int
	Restocks int
	Clamped  int
	Rejected int
}

// DynamicPriceEngine owns the item table, the player wallets and the restock
// anchors.
type DynamicPriceEngine struct {
	cfg PriceConfig
}

// NewDynamicPriceEngine builds an engine from the shop policy.
func NewDynamicPriceEngine(cfg PriceConfig) *DynamicPriceEngine {
	return &DynamicPriceEngine{cfg: cfg}
}

// SetBasePrice registers an item or replaces its base price.
func (e *DynamicPriceEngine) SetBasePrice(item string, base int64) error {
	return errNotImplemented
}

// SetTarget sets the stock level that prices the item at its base price.
func (e *DynamicPriceEngine) SetTarget(item string, target int) error {
	return errNotImplemented
}

// SetStock overwrites the stock of one item.
func (e *DynamicPriceEngine) SetStock(item string, stock int) error {
	return errNotImplemented
}

// SetBand sets the closed price interval of one item.
func (e *DynamicPriceEngine) SetBand(item string, min, max int64) error {
	return errNotImplemented
}

// SetElasticity sets how strongly stock moves the price.
func (e *DynamicPriceEngine) SetElasticity(item string, elasticity float64) error {
	return errNotImplemented
}

// Deposit credits a player wallet.
func (e *DynamicPriceEngine) Deposit(player string, amount int64) error {
	return errNotImplemented
}

// Balance reads a player wallet.
func (e *DynamicPriceEngine) Balance(player string) int64 {
	return 0
}

// Quote computes the price of one side for one quantity.
func (e *DynamicPriceEngine) Quote(item string, side Side, qty int) (Quote, error) {
	return Quote{}, errNotImplemented
}

// Buy moves goods from the shop to the player at the current ask.
func (e *DynamicPriceEngine) Buy(player string, item string, qty int) (TradeResult, error) {
	return TradeResult{}, errNotImplemented
}

// Sell moves goods from the player to the shop at the current bid.
func (e *DynamicPriceEngine) Sell(player string, item string, qty int) (TradeResult, error) {
	return TradeResult{}, errNotImplemented
}

// Restock settles every item that is due at now.
func (e *DynamicPriceEngine) Restock(now int64) (RestockReport, error) {
	return RestockReport{}, errNotImplemented
}

// Stats reads the counters of the engine.
func (e *DynamicPriceEngine) Stats() PriceStats {
	return PriceStats{}
}

// Snapshot serialises the item table, the wallets and the anchors.
func (e *DynamicPriceEngine) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (e *DynamicPriceEngine) Restore(data []byte) error {
	return errNotImplemented
}
