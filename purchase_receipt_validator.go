// Package task validates in app purchase receipts and drives the order state
// machine: it verifies the platform signature, grants the product exactly once
// and keeps the refund clawback ledger.
package task

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Order states.
const (
	StatusSubmitted = "submitted"
	StatusVerified  = "verified"
	StatusGranted   = "granted"
	StatusRefunded  = "refunded"
	StatusRejected  = "rejected"
)

// Verify result statuses.
const (
	VerifyVerified  = "verified"
	VerifyRetryable = "retryable"
	VerifyRejected  = "rejected"
)

// Errors reported by the validator.
var (
	ErrBadReceipt          = errors.New("receipt is invalid")
	ErrBadCatalog          = errors.New("product catalog is invalid")
	ErrBadKey              = errors.New("platform key is required")
	ErrUnknownProduct      = errors.New("unknown product")
	ErrEnvironmentMismatch = errors.New("receipt environment does not match the store mode")
	ErrUnknownOrder        = errors.New("unknown order")
	ErrOrderNotVerified    = errors.New("order is not verified")
	ErrOrderNotGranted     = errors.New("order is not granted")
	ErrBadReason           = errors.New("refund reason is required")
	ErrBadReconcile        = errors.New("reconcile range is invalid")
	ErrBadSnapshot         = errors.New("snapshot payload is invalid")
)

// Receipt is one platform purchase receipt.
type Receipt struct {
	Platform      string
	TransactionID string
	ProductID     string
	PlayerID      string
	Amount        int64
	PurchasedAt   int64
	Signature     string
	Sandbox       bool
}

// Product is one entry of the sale catalog.
type Product struct {
	ID    string
	Units int
	Price int64
}

// VerifyResult is the outcome of one signature check.
type VerifyResult struct {
	OrderID   string
	Status    string
	Reason    string
	Grantable bool
}

// GrantResult is the outcome of one grant.
type GrantResult struct {
	OrderID   string
	PlayerID  string
	ProductID string
	Units     int
	Balance   int64
	Status    string
}

// RefundResult is the outcome of one refund clawback.
type RefundResult struct {
	OrderID    string
	PlayerID   string
	ClawedBack int
	Balance    int64
	Status     string
}

// ReconcileReport summarises one replenishment scan.
type ReconcileReport struct {
	Scanned int
	Granted int
	Skipped int
	Failed  int
}

// IAPStats aggregates the order counters.
type IAPStats struct {
	Submitted       int
	Verified        int
	Granted         int
	Refunded        int
	Rejected        int
	UnitsGranted    int
	UnitsClawedBack int
}

// IAPLimits bounds the validator.
type IAPLimits struct {
	MaxReconcileBatch int
	MaxRetries        int
}

type orderRecord struct {
	ID             string
	Seq            int64
	Receipt        Receipt
	Units          int
	Status         string
	VerifyStatus   string
	VerifyReason   string
	RetryCount     int
	RetryExhausted bool
	GrantResult    *GrantResult
}

// PurchaseReceiptValidator owns the catalog, the orders and the balance ledger.
type PurchaseReceiptValidator struct {
	limits IAPLimits

	mu sync.RWMutex

	catalog      map[string]Product
	platformKeys map[string]string
	sandbox      bool

	orders    map[string]*orderRecord
	txnOrders map[string]string
	nextSeq   int64

	balances map[string]int64
	stats    IAPStats

	reconcileActive bool
	reconcileFrom   int64
	reconcileTo     int64
	reconcileSeq    int64
}

// NewPurchaseReceiptValidator builds a validator with the given caps.
func NewPurchaseReceiptValidator(limits IAPLimits) *PurchaseReceiptValidator {
	return &PurchaseReceiptValidator{
		limits:       limits,
		catalog:      make(map[string]Product),
		platformKeys: make(map[string]string),
		orders:       make(map[string]*orderRecord),
		txnOrders:    make(map[string]string),
		nextSeq:      1,
		balances:     make(map[string]int64),
	}
}

// SetProductCatalog installs the sale catalog in one swap.
func (v *PurchaseReceiptValidator) SetProductCatalog(cat []Product) error {
	next := make(map[string]Product, len(cat))
	for _, product := range cat {
		if strings.TrimSpace(product.ID) == "" || product.Units <= 0 || product.Price < 0 {
			return ErrBadCatalog
		}
		if _, exists := next[product.ID]; exists {
			return ErrBadCatalog
		}
		next[product.ID] = product
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.catalog = next
	return nil
}

// SetPlatformKey installs the shared secret of one store platform.
func (v *PurchaseReceiptValidator) SetPlatformKey(platform, key string) error {
	if strings.TrimSpace(platform) == "" || strings.TrimSpace(key) == "" {
		return ErrBadKey
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if v.platformKeys == nil {
		v.platformKeys = make(map[string]string)
	}
	v.platformKeys[platform] = key
	return nil
}

// SetSandbox switches the store mode between production and sandbox.
func (v *PurchaseReceiptValidator) SetSandbox(on bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.sandbox = on
	return nil
}

// Submit records one receipt and returns its order id.
func (v *PurchaseReceiptValidator) Submit(rcpt Receipt) (string, error) {
	if !validReceiptShape(rcpt) {
		return "", ErrBadReceipt
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.initLocked()

	if orderID, ok := v.txnOrders[transactionKey(rcpt.Platform, rcpt.TransactionID)]; ok {
		return orderID, nil
	}
	if v.sandbox != rcpt.Sandbox {
		return "", ErrEnvironmentMismatch
	}
	product, ok := v.catalog[rcpt.ProductID]
	if !ok {
		return "", ErrUnknownProduct
	}

	seq := v.nextSeq
	v.nextSeq++
	orderID := fmt.Sprintf("ord-%d", seq)
	order := &orderRecord{
		ID:      orderID,
		Seq:     seq,
		Receipt: rcpt,
		Units:   product.Units,
		Status:  StatusSubmitted,
	}
	v.orders[orderID] = order
	v.txnOrders[transactionKey(rcpt.Platform, rcpt.TransactionID)] = orderID
	v.stats.Submitted++
	return orderID, nil
}

// Verify checks the signature of one order.
func (v *PurchaseReceiptValidator) Verify(orderID string) (VerifyResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.initLocked()

	order, ok := v.orders[orderID]
	if !ok {
		return VerifyResult{OrderID: orderID}, ErrUnknownOrder
	}
	return v.verifyLocked(order)
}

// Grant delivers the product of one verified order exactly once.
func (v *PurchaseReceiptValidator) Grant(orderID string) (GrantResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.initLocked()

	order, ok := v.orders[orderID]
	if !ok {
		return GrantResult{OrderID: orderID}, ErrUnknownOrder
	}
	if order.Status == StatusGranted && order.GrantResult != nil {
		return *order.GrantResult, nil
	}
	if order.Status != StatusVerified {
		return GrantResult{OrderID: orderID}, ErrOrderNotVerified
	}
	return v.grantLocked(order), nil
}

// Refund reverses one granted order and claws the units back.
func (v *PurchaseReceiptValidator) Refund(orderID string, reason string) (RefundResult, error) {
	if strings.TrimSpace(reason) == "" {
		return RefundResult{OrderID: orderID}, ErrBadReason
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.initLocked()

	order, ok := v.orders[orderID]
	if !ok {
		return RefundResult{OrderID: orderID}, ErrUnknownOrder
	}
	if order.Status != StatusGranted {
		return RefundResult{OrderID: orderID}, ErrOrderNotGranted
	}

	balance := v.balances[order.Receipt.PlayerID]
	clawedBack := order.Units
	if balance < int64(clawedBack) {
		if balance < 0 {
			clawedBack = 0
		} else {
			clawedBack = int(balance)
		}
	}
	balance -= int64(clawedBack)
	v.balances[order.Receipt.PlayerID] = balance
	order.Status = StatusRefunded
	v.stats.Refunded++
	v.stats.UnitsClawedBack += clawedBack

	return RefundResult{
		OrderID:    order.ID,
		PlayerID:   order.Receipt.PlayerID,
		ClawedBack: clawedBack,
		Balance:    balance,
		Status:     StatusRefunded,
	}, nil
}

// Reconcile retries the pending orders of one purchase time range.
func (v *PurchaseReceiptValidator) Reconcile(from, to int64) (ReconcileReport, error) {
	if from > to {
		return ReconcileReport{}, ErrBadReconcile
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.initLocked()

	if !v.reconcileActive || v.reconcileFrom != from || v.reconcileTo != to {
		v.reconcileActive = true
		v.reconcileFrom = from
		v.reconcileTo = to
		v.reconcileSeq = 0
	}

	orders := v.sortedOrdersLocked()
	if v.reconcileSeq > 0 &&
		!hasEligibleAfterLocked(orders, from, to, v.reconcileSeq) &&
		hasEligibleInRangeLocked(orders, from, to) {
		// A previous pass ended on a retryable order. Start a new pass so it
		// can be retried while keeping the linear cursor for active batches.
		v.reconcileSeq = 0
	}

	var report ReconcileReport
	processed := 0
	maxBatch := v.limits.MaxReconcileBatch

	for _, order := range orders {
		if order.Receipt.PurchasedAt < from || order.Receipt.PurchasedAt > to {
			continue
		}
		if order.Seq <= v.reconcileSeq {
			continue
		}

		if isTerminalLocked(order) || order.RetryExhausted {
			report.Scanned++
			report.Skipped++
			v.reconcileSeq = order.Seq
			continue
		}

		if processed >= maxBatch {
			break
		}
		report.Scanned++

		if order.Status == StatusSubmitted {
			if v.limits.MaxRetries <= 0 || order.RetryCount >= v.limits.MaxRetries {
				order.RetryExhausted = true
				report.Skipped++
				v.reconcileSeq = order.Seq
				continue
			}
			order.RetryCount++
		}

		processed++
		v.reconcileSeq = order.Seq

		switch order.Status {
		case StatusVerified:
			v.grantLocked(order)
			report.Granted++
		case StatusSubmitted:
			result, err := v.verifyLocked(order)
			if err != nil {
				report.Failed++
				continue
			}
			if result.Status == VerifyVerified {
				v.grantLocked(order)
				report.Granted++
				continue
			}
			report.Failed++
			if result.Status == VerifyRetryable &&
				v.limits.MaxRetries > 0 &&
				order.RetryCount >= v.limits.MaxRetries {
				order.RetryExhausted = true
			}
		default:
			report.Failed++
		}
	}

	return report, nil
}

// Balance reports the unit balance of one player.
func (v *PurchaseReceiptValidator) Balance(playerID string) int64 {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.balances[playerID]
}

// Stats returns a copy of the order counters.
func (v *PurchaseReceiptValidator) Stats() IAPStats {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.stats
}

type snapshotOrder struct {
	ID             string       `json:"id"`
	Seq            int64        `json:"seq"`
	Receipt        Receipt      `json:"receipt"`
	Units          int          `json:"units"`
	Status         string       `json:"status"`
	VerifyStatus   string       `json:"verify_status,omitempty"`
	VerifyReason   string       `json:"verify_reason,omitempty"`
	RetryCount     int          `json:"retry_count,omitempty"`
	RetryExhausted bool         `json:"retry_exhausted,omitempty"`
	GrantResult    *GrantResult `json:"grant_result,omitempty"`
}

type snapshotCursor struct {
	Active bool  `json:"active"`
	From   int64 `json:"from"`
	To     int64 `json:"to"`
	Seq    int64 `json:"seq"`
}

type snapshotState struct {
	Version      int               `json:"version"`
	Catalog      []Product         `json:"catalog"`
	PlatformKeys map[string]string `json:"platform_keys"`
	Sandbox      bool              `json:"sandbox"`
	Orders       []snapshotOrder   `json:"orders"`
	Balances     map[string]int64  `json:"balances"`
	NextOrder    int64             `json:"next_order"`
	Reconcile    snapshotCursor    `json:"reconcile"`
	Stats        IAPStats          `json:"stats"`
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

// Snapshot serialises the catalog, the orders and the ledger.
func (v *PurchaseReceiptValidator) Snapshot() ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	nextOrder := v.nextSeq
	if nextOrder < 1 {
		nextOrder = 1
	}
	state := snapshotState{
		Version:      1,
		Catalog:      make([]Product, 0, len(v.catalog)),
		PlatformKeys: make(map[string]string, len(v.platformKeys)),
		Sandbox:      v.sandbox,
		Orders:       make([]snapshotOrder, 0, len(v.orders)),
		Balances:     make(map[string]int64, len(v.balances)),
		NextOrder:    nextOrder,
		Reconcile: snapshotCursor{
			Active: v.reconcileActive,
			From:   v.reconcileFrom,
			To:     v.reconcileTo,
			Seq:    v.reconcileSeq,
		},
		Stats: v.stats,
	}
	for _, product := range v.catalog {
		state.Catalog = append(state.Catalog, product)
	}
	sort.Slice(state.Catalog, func(i, j int) bool {
		return state.Catalog[i].ID < state.Catalog[j].ID
	})
	for platform, key := range v.platformKeys {
		state.PlatformKeys[platform] = key
	}
	for _, order := range v.orders {
		var grantResult *GrantResult
		if order.GrantResult != nil {
			copy := *order.GrantResult
			grantResult = &copy
		}
		state.Orders = append(state.Orders, snapshotOrder{
			ID:             order.ID,
			Seq:            order.Seq,
			Receipt:        order.Receipt,
			Units:          order.Units,
			Status:         order.Status,
			VerifyStatus:   order.VerifyStatus,
			VerifyReason:   order.VerifyReason,
			RetryCount:     order.RetryCount,
			RetryExhausted: order.RetryExhausted,
			GrantResult:    grantResult,
		})
	}
	sort.Slice(state.Orders, func(i, j int) bool {
		return state.Orders[i].Seq < state.Orders[j].Seq
	})
	for playerID, balance := range v.balances {
		state.Balances[playerID] = balance
	}

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	checksum := sha256.Sum256(payload)
	data, err := json.Marshal(snapshotEnvelope{
		Version:  1,
		Payload:  payload,
		Checksum: hex.EncodeToString(checksum[:]),
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the validator state with a snapshot, all or nothing.
func (v *PurchaseReceiptValidator) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Version != 1 || len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	checksum := sha256.Sum256(envelope.Payload)
	if !hmac.Equal([]byte(envelope.Checksum), []byte(hex.EncodeToString(checksum[:]))) {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.Payload, &state); err != nil {
		return ErrBadSnapshot
	}
	if state.Version != 1 || state.NextOrder < 1 ||
		state.Catalog == nil || state.PlatformKeys == nil ||
		state.Orders == nil || state.Balances == nil {
		return ErrBadSnapshot
	}

	catalog, err := catalogFromSnapshot(state.Catalog)
	if err != nil {
		return ErrBadSnapshot
	}
	for platform, key := range state.PlatformKeys {
		if strings.TrimSpace(platform) == "" || strings.TrimSpace(key) == "" {
			return ErrBadSnapshot
		}
	}

	orders := make(map[string]*orderRecord, len(state.Orders))
	txnOrders := make(map[string]string, len(state.Orders))
	var maxSeq int64
	for _, snapshot := range state.Orders {
		if snapshot.Seq <= 0 || snapshot.ID != fmt.Sprintf("ord-%d", snapshot.Seq) {
			return ErrBadSnapshot
		}
		if !validReceiptShape(snapshot.Receipt) || snapshot.Units <= 0 {
			return ErrBadSnapshot
		}
		if snapshot.RetryCount < 0 {
			return ErrBadSnapshot
		}
		if !validSnapshotStatus(snapshot.Status, snapshot.VerifyStatus, snapshot.GrantResult) {
			return ErrBadSnapshot
		}
		if snapshot.Status == StatusGranted || snapshot.Status == StatusRefunded {
			if snapshot.GrantResult.OrderID != snapshot.ID ||
				snapshot.GrantResult.PlayerID != snapshot.Receipt.PlayerID ||
				snapshot.GrantResult.ProductID != snapshot.Receipt.ProductID ||
				snapshot.GrantResult.Units != snapshot.Units ||
				snapshot.GrantResult.Balance < 0 ||
				snapshot.GrantResult.Status != StatusGranted {
				return ErrBadSnapshot
			}
		}
		if _, exists := orders[snapshot.ID]; exists {
			return ErrBadSnapshot
		}
		key := transactionKey(snapshot.Receipt.Platform, snapshot.Receipt.TransactionID)
		if _, exists := txnOrders[key]; exists {
			return ErrBadSnapshot
		}
		if snapshot.Seq > maxSeq {
			maxSeq = snapshot.Seq
		}

		grantResult := snapshot.GrantResult
		if grantResult != nil {
			copy := *grantResult
			grantResult = &copy
		}
		orders[snapshot.ID] = &orderRecord{
			ID:             snapshot.ID,
			Seq:            snapshot.Seq,
			Receipt:        snapshot.Receipt,
			Units:          snapshot.Units,
			Status:         snapshot.Status,
			VerifyStatus:   snapshot.VerifyStatus,
			VerifyReason:   snapshot.VerifyReason,
			RetryCount:     snapshot.RetryCount,
			RetryExhausted: snapshot.RetryExhausted,
			GrantResult:    grantResult,
		}
		txnOrders[key] = snapshot.ID
	}
	if state.NextOrder <= maxSeq {
		return ErrBadSnapshot
	}
	for playerID, balance := range state.Balances {
		if strings.TrimSpace(playerID) == "" || balance < 0 {
			return ErrBadSnapshot
		}
	}
	if state.Stats.Submitted < 0 || state.Stats.Verified < 0 ||
		state.Stats.Granted < 0 || state.Stats.Refunded < 0 ||
		state.Stats.Rejected < 0 || state.Stats.UnitsGranted < 0 ||
		state.Stats.UnitsClawedBack < 0 {
		return ErrBadSnapshot
	}
	if state.Reconcile.Active {
		if state.Reconcile.From > state.Reconcile.To ||
			state.Reconcile.Seq < 0 || state.Reconcile.Seq > maxSeq {
			return ErrBadSnapshot
		}
	} else if state.Reconcile.Seq != 0 {
		return ErrBadSnapshot
	}

	balances := make(map[string]int64, len(state.Balances))
	for playerID, balance := range state.Balances {
		balances[playerID] = balance
	}
	platformKeys := make(map[string]string, len(state.PlatformKeys))
	for platform, key := range state.PlatformKeys {
		platformKeys[platform] = key
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.catalog = catalog
	v.platformKeys = platformKeys
	v.sandbox = state.Sandbox
	v.orders = orders
	v.txnOrders = txnOrders
	v.nextSeq = state.NextOrder
	v.balances = balances
	v.stats = state.Stats
	v.reconcileActive = state.Reconcile.Active
	v.reconcileFrom = state.Reconcile.From
	v.reconcileTo = state.Reconcile.To
	v.reconcileSeq = state.Reconcile.Seq
	return nil
}

func (v *PurchaseReceiptValidator) initLocked() {
	if v.catalog == nil {
		v.catalog = make(map[string]Product)
	}
	if v.platformKeys == nil {
		v.platformKeys = make(map[string]string)
	}
	if v.orders == nil {
		v.orders = make(map[string]*orderRecord)
	}
	if v.txnOrders == nil {
		v.txnOrders = make(map[string]string)
	}
	if v.balances == nil {
		v.balances = make(map[string]int64)
	}
	if v.nextSeq < 1 {
		v.nextSeq = 1
	}
}

func validReceiptShape(receipt Receipt) bool {
	return strings.TrimSpace(receipt.Platform) != "" &&
		strings.TrimSpace(receipt.TransactionID) != "" &&
		strings.TrimSpace(receipt.ProductID) != "" &&
		strings.TrimSpace(receipt.PlayerID) != "" &&
		receipt.Amount > 0 &&
		receipt.PurchasedAt > 0 &&
		strings.TrimSpace(receipt.Signature) != ""
}

func transactionKey(platform, transactionID string) string {
	return platform + "\x00" + transactionID
}

func (v *PurchaseReceiptValidator) verifyLocked(order *orderRecord) (VerifyResult, error) {
	if v.sandbox != order.Receipt.Sandbox {
		return VerifyResult{OrderID: order.ID}, ErrEnvironmentMismatch
	}

	result := VerifyResult{OrderID: order.ID}
	switch order.Status {
	case StatusRejected:
		result.Status = VerifyRejected
		result.Reason = order.VerifyReason
		return result, nil
	case StatusVerified, StatusGranted, StatusRefunded:
		result.Status = VerifyVerified
		result.Reason = order.VerifyReason
		result.Grantable = order.Status == StatusVerified
		return result, nil
	}

	key, ok := v.platformKeys[order.Receipt.Platform]
	if !ok {
		order.VerifyStatus = VerifyRetryable
		order.VerifyReason = "platform key is not configured"
		result.Status = VerifyRetryable
		result.Reason = order.VerifyReason
		return result, nil
	}

	if _, err := hex.DecodeString(order.Receipt.Signature); err != nil {
		order.VerifyStatus = VerifyRetryable
		order.VerifyReason = "signature format is invalid"
		result.Status = VerifyRetryable
		result.Reason = order.VerifyReason
		return result, nil
	}

	expected := signReceipt(key, order.Receipt)
	if !hmac.Equal([]byte(expected), []byte(order.Receipt.Signature)) {
		order.Status = StatusRejected
		order.VerifyStatus = VerifyRejected
		order.VerifyReason = "signature mismatch"
		v.stats.Rejected++
		result.Status = VerifyRejected
		result.Reason = order.VerifyReason
		return result, nil
	}

	order.Status = StatusVerified
	order.VerifyStatus = VerifyVerified
	order.VerifyReason = ""
	order.RetryExhausted = false
	v.stats.Verified++
	result.Status = VerifyVerified
	result.Grantable = true
	return result, nil
}

func (v *PurchaseReceiptValidator) grantLocked(order *orderRecord) GrantResult {
	balance := v.balances[order.Receipt.PlayerID] + int64(order.Units)
	v.balances[order.Receipt.PlayerID] = balance
	order.Status = StatusGranted
	result := GrantResult{
		OrderID:   order.ID,
		PlayerID:  order.Receipt.PlayerID,
		ProductID: order.Receipt.ProductID,
		Units:     order.Units,
		Balance:   balance,
		Status:    StatusGranted,
	}
	order.GrantResult = &result
	v.stats.Granted++
	v.stats.UnitsGranted += order.Units
	return result
}

func signReceipt(key string, receipt Receipt) string {
	canonical := fmt.Sprintf("%s|%s|%s|%s|%d|%d",
		receipt.Platform,
		receipt.TransactionID,
		receipt.ProductID,
		receipt.PlayerID,
		receipt.Amount,
		receipt.PurchasedAt,
	)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

func isTerminalLocked(order *orderRecord) bool {
	switch order.Status {
	case StatusGranted, StatusRefunded, StatusRejected:
		return true
	default:
		return false
	}
}

func (v *PurchaseReceiptValidator) sortedOrdersLocked() []*orderRecord {
	orders := make([]*orderRecord, 0, len(v.orders))
	for _, order := range v.orders {
		orders = append(orders, order)
	}
	sort.Slice(orders, func(i, j int) bool {
		return orders[i].Seq < orders[j].Seq
	})
	return orders
}

func hasEligibleAfterLocked(orders []*orderRecord, from, to, seq int64) bool {
	for _, order := range orders {
		if order.Seq <= seq {
			continue
		}
		if order.Receipt.PurchasedAt < from || order.Receipt.PurchasedAt > to {
			continue
		}
		if isEligibleLocked(order) {
			return true
		}
	}
	return false
}

func hasEligibleInRangeLocked(orders []*orderRecord, from, to int64) bool {
	for _, order := range orders {
		if order.Receipt.PurchasedAt < from || order.Receipt.PurchasedAt > to {
			continue
		}
		if isEligibleLocked(order) {
			return true
		}
	}
	return false
}

func isEligibleLocked(order *orderRecord) bool {
	if order.RetryExhausted {
		return false
	}
	return order.Status == StatusVerified || order.Status == StatusSubmitted
}

func catalogFromSnapshot(products []Product) (map[string]Product, error) {
	catalog := make(map[string]Product, len(products))
	for _, product := range products {
		if strings.TrimSpace(product.ID) == "" || product.Units <= 0 || product.Price < 0 {
			return nil, ErrBadSnapshot
		}
		if _, exists := catalog[product.ID]; exists {
			return nil, ErrBadSnapshot
		}
		catalog[product.ID] = product
	}
	return catalog, nil
}

func validSnapshotStatus(status, verifyStatus string, grant *GrantResult) bool {
	switch status {
	case StatusSubmitted:
		return (verifyStatus == "" || verifyStatus == VerifyRetryable) && grant == nil
	case StatusVerified:
		return verifyStatus == VerifyVerified && grant == nil
	case StatusGranted, StatusRefunded:
		return verifyStatus == VerifyVerified && grant != nil
	case StatusRejected:
		return verifyStatus == VerifyRejected && grant == nil
	default:
		return false
	}
}
