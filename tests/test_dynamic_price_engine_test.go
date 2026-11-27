package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-a-22"
)

func priceConfig(now *int64) impl.PriceConfig {
	return impl.PriceConfig{
		Now:               func() int64 { return *now },
		QuoteTTL:          5000,
		RestockPeriod:     1000,
		RestockQty:        40,
		SellMarginPercent: 20,
		MaxStock:          1000,
		MaxHistory:        32,
		DefaultBand:       impl.Band{Min: 1, Max: 1000000},
	}
}

func newPriceEngine(t *testing.T) (*impl.DynamicPriceEngine, *int64) {
	t.Helper()
	now := int64(0)
	e := impl.NewDynamicPriceEngine(priceConfig(&now))
	if e == nil {
		t.Fatalf("engine must not be nil")
	}
	return e, &now
}

func seedOre(t *testing.T, e *impl.DynamicPriceEngine) {
	t.Helper()
	if err := e.SetBasePrice("ore", 37); err != nil {
		t.Fatalf("set base price: %v", err)
	}
	if err := e.SetTarget("ore", 1000); err != nil {
		t.Fatalf("set target: %v", err)
	}
	if err := e.SetElasticity("ore", 0.5); err != nil {
		t.Fatalf("set elasticity: %v", err)
	}
	if err := e.SetStock("ore", 630); err != nil {
		t.Fatalf("set stock: %v", err)
	}
}

func TestSetBasePriceRejectsBadInput(t *testing.T) {
	e, _ := newPriceEngine(t)
	if err := e.SetBasePrice("  ", 100); !errors.Is(err, impl.ErrBadItem) {
		t.Fatalf("blank item = %v, want ErrBadItem", err)
	}
	if err := e.SetBasePrice("ore", 0); !errors.Is(err, impl.ErrBadBase) {
		t.Fatalf("base 0 = %v, want ErrBadBase", err)
	}
	if err := e.SetBasePrice("ore", -5); !errors.Is(err, impl.ErrBadBase) {
		t.Fatalf("base -5 = %v, want ErrBadBase", err)
	}
	if err := e.SetBasePrice("ore", 37); err != nil {
		t.Fatalf("base 37 = %v, want nil", err)
	}
	if stats := e.Stats(); stats.Items != 1 {
		t.Fatalf("item count = %d, want 1", stats.Items)
	}
}

func TestSetTargetAndStockRejectBadValues(t *testing.T) {
	e, _ := newPriceEngine(t)
	if err := e.SetBasePrice("ore", 37); err != nil {
		t.Fatalf("set base price: %v", err)
	}
	if err := e.SetTarget("ore", 0); !errors.Is(err, impl.ErrBadTarget) {
		t.Fatalf("target 0 = %v, want ErrBadTarget", err)
	}
	if err := e.SetTarget("ore", -10); !errors.Is(err, impl.ErrBadTarget) {
		t.Fatalf("target -10 = %v, want ErrBadTarget", err)
	}
	if err := e.SetStock("ore", -1); !errors.Is(err, impl.ErrBadQty) {
		t.Fatalf("stock -1 = %v, want ErrBadQty", err)
	}
	if err := e.SetStock("ore", 1001); !errors.Is(err, impl.ErrStockFull) {
		t.Fatalf("stock above the cap = %v, want ErrStockFull", err)
	}
	if err := e.SetTarget("gem", 100); !errors.Is(err, impl.ErrUnknownItem) {
		t.Fatalf("target of an unknown item = %v, want ErrUnknownItem", err)
	}
	if err := e.SetStock("ore", 500); err != nil {
		t.Fatalf("stock 500 = %v, want nil", err)
	}
}

func TestSetBandRejectsInvertedBand(t *testing.T) {
	e, _ := newPriceEngine(t)
	if err := e.SetBasePrice("ore", 100); err != nil {
		t.Fatalf("set base price: %v", err)
	}
	if err := e.SetBand("ore", 50, 40); !errors.Is(err, impl.ErrBadBand) {
		t.Fatalf("inverted band = %v, want ErrBadBand", err)
	}
	if err := e.SetBand("ore", 0, 40); !errors.Is(err, impl.ErrBadBand) {
		t.Fatalf("band with a zero floor = %v, want ErrBadBand", err)
	}
	if err := e.SetBand("gem", 1, 10); !errors.Is(err, impl.ErrUnknownItem) {
		t.Fatalf("band of an unknown item = %v, want ErrUnknownItem", err)
	}
	if err := e.SetBand("ore", 30, 60); err != nil {
		t.Fatalf("band 30..60 = %v, want nil", err)
	}
}

func TestSetElasticityRejectsOutOfRange(t *testing.T) {
	e, _ := newPriceEngine(t)
	if err := e.SetBasePrice("ore", 100); err != nil {
		t.Fatalf("set base price: %v", err)
	}
	if err := e.SetElasticity("ore", -0.1); !errors.Is(err, impl.ErrBadElasticity) {
		t.Fatalf("elasticity -0.1 = %v, want ErrBadElasticity", err)
	}
	if err := e.SetElasticity("ore", 4.5); !errors.Is(err, impl.ErrBadElasticity) {
		t.Fatalf("elasticity 4.5 = %v, want ErrBadElasticity", err)
	}
	if err := e.SetElasticity("gem", 0.5); !errors.Is(err, impl.ErrUnknownItem) {
		t.Fatalf("elasticity of an unknown item = %v, want ErrUnknownItem", err)
	}
	if err := e.SetElasticity("ore", 0.5); err != nil {
		t.Fatalf("elasticity 0.5 = %v, want nil", err)
	}
}

func TestQuoteRoundsBuyUpSellDown(t *testing.T) {
	e, _ := newPriceEngine(t)
	seedOre(t, e)
	buy, err := e.Quote("ore", impl.SideBuy, 2)
	if err != nil {
		t.Fatalf("buy quote: %v", err)
	}
	if buy.UnitPrice != 44 {
		t.Fatalf("ask = %d, want 44", buy.UnitPrice)
	}
	if buy.Total != 88 {
		t.Fatalf("ask total = %d, want 88", buy.Total)
	}
	if buy.Stock != 630 || buy.Qty != 2 || buy.Side != impl.SideBuy {
		t.Fatalf("quote echo = %+v, want ore buy 2 at stock 630", buy)
	}
	if buy.Version < 1 {
		t.Fatalf("quote version = %d, want a positive revision", buy.Version)
	}
	if buy.ExpiresAt != 5000 {
		t.Fatalf("quote expiry = %d, want 5000", buy.ExpiresAt)
	}
	sell, err := e.Quote("ore", impl.SideSell, 2)
	if err != nil {
		t.Fatalf("sell quote: %v", err)
	}
	if sell.UnitPrice != 35 {
		t.Fatalf("bid = %d, want 35", sell.UnitPrice)
	}
	if sell.Total != 70 {
		t.Fatalf("bid total = %d, want 70", sell.Total)
	}
}

func TestQuoteClampsToBand(t *testing.T) {
	e, _ := newPriceEngine(t)
	if err := e.SetBasePrice("ore", 100); err != nil {
		t.Fatalf("set base price: %v", err)
	}
	if err := e.SetTarget("ore", 1000); err != nil {
		t.Fatalf("set target: %v", err)
	}
	if err := e.SetElasticity("ore", 0.5); err != nil {
		t.Fatalf("set elasticity: %v", err)
	}
	if err := e.SetStock("ore", 300); err != nil {
		t.Fatalf("set stock: %v", err)
	}
	if err := e.SetBand("ore", 50, 120); err != nil {
		t.Fatalf("set band: %v", err)
	}
	buy, err := e.Quote("ore", impl.SideBuy, 1)
	if err != nil {
		t.Fatalf("buy quote: %v", err)
	}
	if buy.UnitPrice != 120 {
		t.Fatalf("clamped ask = %d, want 120", buy.UnitPrice)
	}
	sell, err := e.Quote("ore", impl.SideSell, 1)
	if err != nil {
		t.Fatalf("sell quote: %v", err)
	}
	if sell.UnitPrice != 108 {
		t.Fatalf("bid = %d, want 108", sell.UnitPrice)
	}
	if stats := e.Stats(); stats.Clamped != 1 {
		t.Fatalf("clamp counter = %d, want 1", stats.Clamped)
	}
}

func TestQuoteRejectsUnknownItemBadQtyAndSide(t *testing.T) {
	e, _ := newPriceEngine(t)
	seedOre(t, e)
	if _, err := e.Quote("gem", impl.SideBuy, 1); !errors.Is(err, impl.ErrUnknownItem) {
		t.Fatalf("quote of an unknown item = %v, want ErrUnknownItem", err)
	}
	if _, err := e.Quote("ore", impl.SideBuy, 0); !errors.Is(err, impl.ErrBadQty) {
		t.Fatalf("quote of zero units = %v, want ErrBadQty", err)
	}
	if _, err := e.Quote("ore", impl.Side("hold"), 1); !errors.Is(err, impl.ErrBadSide) {
		t.Fatalf("quote on an unknown side = %v, want ErrBadSide", err)
	}
}

func TestBuyDecreasesStockAndChargesAsk(t *testing.T) {
	e, _ := newPriceEngine(t)
	seedOre(t, e)
	if err := e.Deposit("p1", 500); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	r, err := e.Buy("p1", "ore", 2)
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	if r.UnitPrice != 44 || r.Total != 88 {
		t.Fatalf("buy price = %d x %d, want 44 x 2", r.UnitPrice, r.Qty)
	}
	if r.StockAfter != 628 {
		t.Fatalf("stock after buy = %d, want 628", r.StockAfter)
	}
	if r.Balance != 412 {
		t.Fatalf("balance after buy = %d, want 412", r.Balance)
	}
	if got := e.Balance("p1"); got != 412 {
		t.Fatalf("wallet = %d, want 412", got)
	}
	if stats := e.Stats(); stats.Trades != 1 {
		t.Fatalf("trade counter = %d, want 1", stats.Trades)
	}
}

func TestBuyRejectsInsufficientStockAndGold(t *testing.T) {
	e, _ := newPriceEngine(t)
	seedOre(t, e)
	if err := e.Deposit("rich", 1000); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if _, err := e.Buy("rich", "ore", 1000); !errors.Is(err, impl.ErrInsufficientStock) {
		t.Fatalf("buy beyond stock = %v, want ErrInsufficientStock", err)
	}
	if err := e.Deposit("poor", 10); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if _, err := e.Buy("poor", "ore", 1); !errors.Is(err, impl.ErrInsufficientGold) {
		t.Fatalf("buy without gold = %v, want ErrInsufficientGold", err)
	}
	q, err := e.Quote("ore", impl.SideBuy, 1)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.Stock != 630 {
		t.Fatalf("stock after rejected buys = %d, want 630", q.Stock)
	}
	if stats := e.Stats(); stats.Rejected != 2 {
		t.Fatalf("reject counter = %d, want 2", stats.Rejected)
	}
}

func TestSellIncreasesStockAndPaysBid(t *testing.T) {
	e, _ := newPriceEngine(t)
	seedOre(t, e)
	r, err := e.Sell("p1", "ore", 2)
	if err != nil {
		t.Fatalf("sell: %v", err)
	}
	if r.UnitPrice != 35 || r.Total != 70 {
		t.Fatalf("sell price = %d x %d, want 35 x 2", r.UnitPrice, r.Qty)
	}
	if r.StockAfter != 632 {
		t.Fatalf("stock after sell = %d, want 632", r.StockAfter)
	}
	if r.Balance != 70 {
		t.Fatalf("balance after sell = %d, want 70", r.Balance)
	}
}

func TestSellRejectsOverflowBeyondMaxStock(t *testing.T) {
	now := int64(0)
	cfg := priceConfig(&now)
	cfg.MaxStock = 700
	e := impl.NewDynamicPriceEngine(cfg)
	if err := e.SetBasePrice("ore", 37); err != nil {
		t.Fatalf("set base price: %v", err)
	}
	if err := e.SetTarget("ore", 1000); err != nil {
		t.Fatalf("set target: %v", err)
	}
	if err := e.SetElasticity("ore", 0.5); err != nil {
		t.Fatalf("set elasticity: %v", err)
	}
	if err := e.SetStock("ore", 690); err != nil {
		t.Fatalf("set stock: %v", err)
	}
	if _, err := e.Sell("p1", "ore", 20); !errors.Is(err, impl.ErrStockFull) {
		t.Fatalf("sell past the cap = %v, want ErrStockFull", err)
	}
	q, err := e.Quote("ore", impl.SideBuy, 1)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.Stock != 690 {
		t.Fatalf("stock after the rejected sell = %d, want 690", q.Stock)
	}
}

func TestRoundTripNeverProfits(t *testing.T) {
	e, _ := newPriceEngine(t)
	seedOre(t, e)
	if err := e.Deposit("p1", 1000); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if _, err := e.Buy("p1", "ore", 1); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if _, err := e.Sell("p1", "ore", 1); err != nil {
		t.Fatalf("sell: %v", err)
	}
	final := e.Balance("p1")
	if final != 991 {
		t.Fatalf("balance after a round trip = %d, want 991", final)
	}
	if final >= 1000 {
		t.Fatalf("round trip returned %d, must never beat the starting wallet of 1000", final)
	}
}

func TestRestockSinglePeriodAppliesBatch(t *testing.T) {
	e, now := newPriceEngine(t)
	seedOre(t, e)
	*now = 1000
	rep, err := e.Restock(1000)
	if err != nil {
		t.Fatalf("restock: %v", err)
	}
	if rep.Applied != 40 {
		t.Fatalf("applied units = %d, want 40", rep.Applied)
	}
	if len(rep.Items) != 1 {
		t.Fatalf("restock lines = %+v, want one", rep.Items)
	}
	line := rep.Items[0]
	if line.Item != "ore" || line.Periods != 1 || line.Applied != 40 || line.Stock != 670 {
		t.Fatalf("restock line = %+v, want one period of 40 units reaching stock 670", line)
	}
	q, err := e.Quote("ore", impl.SideBuy, 1)
	if err != nil {
		t.Fatalf("quote after restock: %v", err)
	}
	if q.Stock != 670 {
		t.Fatalf("stock after restock = %d, want 670", q.Stock)
	}
	if stats := e.Stats(); stats.Restocks != 1 {
		t.Fatalf("restock counter = %d, want 1", stats.Restocks)
	}
}

func TestSnapshotRestoreKeepsPricesAndStock(t *testing.T) {
	e, now := newPriceEngine(t)
	seedOre(t, e)
	if err := e.Deposit("p1", 500); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if _, err := e.Buy("p1", "ore", 2); err != nil {
		t.Fatalf("buy: %v", err)
	}
	data, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewDynamicPriceEngine(priceConfig(now))
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	q, err := restored.Quote("ore", impl.SideBuy, 1)
	if err != nil {
		t.Fatalf("quote after restore: %v", err)
	}
	if q.UnitPrice != 44 || q.Stock != 628 {
		t.Fatalf("restored quote = %+v, want ask 44 at stock 628", q)
	}
	if got := restored.Balance("p1"); got != 412 {
		t.Fatalf("restored wallet = %d, want 412", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	e, _ := newPriceEngine(t)
	seedOre(t, e)
	if err := e.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload = %v, want ErrBadSnapshot", err)
	}
	q, err := e.Quote("ore", impl.SideBuy, 1)
	if err != nil {
		t.Fatalf("quote after a failed restore: %v", err)
	}
	if q.UnitPrice != 44 || q.Stock != 630 {
		t.Fatalf("state after a failed restore = %+v, want the original ask 44 at stock 630", q)
	}
}
