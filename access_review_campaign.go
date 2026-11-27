// Package task runs periodic access review campaigns: it freezes a snapshot of
// the grants at open time, collects exactly one effective decision per item,
// and settles the remaining items with the configured default when the
// campaign is closed.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Decision values accepted by Decide.
const (
	DecisionKeep   = "keep"
	DecisionRevoke = "revoke"
)

// Audit actions written by the campaign.
const (
	ActionOpened  = "opened"
	ActionDecided = "decided"
	ActionClosed  = "closed"
	ActionAborted = "aborted"
)

// Errors returned by the campaign.
var (
	ErrBadCampaign     = errors.New("campaign id is required")
	ErrCampaignExists  = errors.New("campaign already exists")
	ErrUnknownCampaign = errors.New("campaign is not known")
	ErrAlreadyClosed   = errors.New("campaign is already closed")
	ErrUnknownItem     = errors.New("grant item is not part of the campaign snapshot")
	ErrAlreadyDecided  = errors.New("item already carries a different decision")
	ErrBadDecision     = errors.New("decision must be keep or revoke")
	ErrCampaignFull    = errors.New("campaign item budget is exhausted")
	ErrBadGrant        = errors.New("account and permission are required")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Grant is one live authorization of one account.
type Grant struct {
	Account    string
	Permission string
}

// Item is one frozen grant inside a campaign.
type Item struct {
	ID         string
	Account    string
	Permission string
	Decision   string
	Reviewer   string
	DecidedAt  time.Time
	Seq        uint64
}

// CampaignState is the public state of one campaign.
type CampaignState struct {
	ID       string
	OpenedAt time.Time
	ClosedAt time.Time
	Total    int
	Decided  int
	Kept     int
	Revoked  int
	Pending  int
	Closed   bool
}

// DecisionResult reports one Decide call.
type DecisionResult struct {
	ItemID   string
	Decision string
	Applied  bool
	Seq      uint64
}

// CloseResult reports what a campaign settled.
type CloseResult struct {
	CampaignID string
	Kept       []string
	Revoked    []string
	Defaulted  []string
	ClosedAt   time.Time
}

// Progress is the running tally of one campaign.
type Progress struct {
	Total   int
	Decided int
	Kept    int
	Revoked int
	Pending int
}

// AuditEntry is one line of the campaign audit trail.
type AuditEntry struct {
	CampaignID string
	Action     string
	ItemID     string
	Detail     string
	Seq        uint64
	At         time.Time
}

// CampaignConfig sets the settlement policy of one guard.
type CampaignConfig struct {
	// DefaultRevoke settles undecided items as revoked when true and as kept
	// when false.
	DefaultRevoke bool
	// MaxItems caps the number of frozen items per campaign.
	MaxItems int
}

// AccessReviewCampaign owns the live grants and every campaign snapshot.
type AccessReviewCampaign struct {
	cfg CampaignConfig
}

// NewAccessReviewCampaign builds a campaign guard with the given policy.
func NewAccessReviewCampaign(cfg CampaignConfig) *AccessReviewCampaign {
	return &AccessReviewCampaign{cfg: cfg}
}

// Grant adds one live authorization.
func (g *AccessReviewCampaign) Grant(account, permission string) error {
	return errNotImplemented
}

// RevokeGrant removes one live authorization.
func (g *AccessReviewCampaign) RevokeGrant(account, permission string) error {
	return errNotImplemented
}

// Open freezes the current grants into a new campaign.
func (g *AccessReviewCampaign) Open(campaignID string, at time.Time) (CampaignState, error) {
	return CampaignState{}, errNotImplemented
}

// Decide records the reviewer decision of one frozen item.
func (g *AccessReviewCampaign) Decide(campaignID, itemID, reviewer, decision string, at time.Time) (DecisionResult, error) {
	return DecisionResult{}, errNotImplemented
}

// Close settles the undecided items with the default policy and freezes the
// campaign result.
func (g *AccessReviewCampaign) Close(campaignID string, at time.Time) (CloseResult, error) {
	return CloseResult{}, errNotImplemented
}

// Progress reports the running tally of one campaign.
func (g *AccessReviewCampaign) Progress(campaignID string) (Progress, error) {
	return Progress{}, errNotImplemented
}

// Items lists the frozen items of one campaign, ordered by account and then by
// permission.
func (g *AccessReviewCampaign) Items(campaignID string) ([]Item, error) {
	return nil, errNotImplemented
}

// Campaign reads the state of one campaign.
func (g *AccessReviewCampaign) Campaign(campaignID string) (CampaignState, bool) {
	return CampaignState{}, false
}

// Abort stops a campaign without settling it.
func (g *AccessReviewCampaign) Abort(campaignID string) error {
	return errNotImplemented
}

// Audit returns the campaign audit trail, oldest first.
func (g *AccessReviewCampaign) Audit(campaignID string) ([]AuditEntry, error) {
	return nil, errNotImplemented
}

// Snapshot serialises the live grants, the campaigns and the sequence
// watermark.
func (g *AccessReviewCampaign) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole guard state with a snapshot, all or nothing.
func (g *AccessReviewCampaign) Restore(data []byte) error {
	return errNotImplemented
}
