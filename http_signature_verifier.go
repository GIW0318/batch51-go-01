// Package task verifies HTTP Message Signatures at the API gateway and keeps
// the key and nonce ledgers that go with them.
package task

import (
	"errors"
	"io"
	"time"
)

// errNotImplemented is returned by the behaviours that are still missing.
var errNotImplemented = errors.New("not implemented")

// Errors surfaced by the verifier.
var (
	ErrBlankKey               = errors.New("key id and secret are required")
	ErrKeyExists              = errors.New("key id is already registered")
	ErrUnknownKey             = errors.New("key id is not registered")
	ErrKeyNotYetValid         = errors.New("key was not valid at signing time")
	ErrKeyExpired             = errors.New("key had expired at signing time")
	ErrKeyRevoked             = errors.New("key was revoked before the signature was created")
	ErrBadSignature           = errors.New("signature does not match the rebuilt base")
	ErrClockSkew              = errors.New("created instant is outside the accepted skew")
	ErrSignatureExpired       = errors.New("signature has already expired")
	ErrDigestMismatch         = errors.New("content digest does not match the body")
	ErrNonceReplay            = errors.New("nonce was already used with this key")
	ErrNonceCapacity          = errors.New("nonce ledger is full for this key")
	ErrMissingMethodComponent = errors.New("signature-input must cover @method")
	ErrDuplicateComponent     = errors.New("signature-input repeats a component")
	ErrComponentCase          = errors.New("header component names must be lowercase")
	ErrUnknownComponent       = errors.New("signature-input names an unsupported component")
	ErrTooManyComponents      = errors.New("signature-input has too many components")
	ErrBaseTooLong            = errors.New("signing base exceeds the configured limit")
	ErrBadSignatureInput      = errors.New("signature-input is empty or malformed")
	ErrBadSnapshot            = errors.New("snapshot payload is invalid")
)

// VerifierConfig bounds the verifier and injects the clock.
type VerifierConfig struct {
	Clock           func() time.Time
	MaxSkew         time.Duration
	MaxValidity     time.Duration
	MaxNoncesPerKey int
	MaxComponents   int
	MaxBaseLen      int
	MaxBody         int
}

// KeyMaterial is one registered verification key.
type KeyMaterial struct {
	KeyID     string
	Subject   string
	Secret    []byte
	NotBefore time.Time
	NotAfter  time.Time
}

// Header is one request header in wire order.
type Header struct {
	Name  string
	Value string
}

// SignedRequest is one inbound request carrying an HTTP Message Signature.
type SignedRequest struct {
	Method         string
	Target         string
	Headers        []Header
	Body           io.Reader
	SignatureInput string
	Signature      string
	KeyID          string
	Created        int64
	Expires        int64
	Nonce          string
	Streaming      bool
}

// Signer is the identity proven by a verified request.
type Signer struct {
	KeyID      string
	Subject    string
	SignedAt   time.Time
	Components []string
	Warning    string
}

// Trace explains how one request was handled.
type Trace struct {
	Base       string
	Components []string
	Accepted   bool
	Reason     string
	Steps      []string
}

// HttpSignatureVerifier owns the key ring, the revocation watermarks and the
// per key nonce ledgers.
type HttpSignatureVerifier struct {
	cfg VerifierConfig
}

// NewHttpSignatureVerifier builds a verifier with the given caps.
func NewHttpSignatureVerifier(cfg VerifierConfig) *HttpSignatureVerifier {
	return &HttpSignatureVerifier{cfg: cfg}
}

// Verify rebuilds the signing base, checks the signature and consumes a nonce.
func (v *HttpSignatureVerifier) Verify(req SignedRequest) (Signer, error) {
	return Signer{}, errNotImplemented
}

// RegisterKey adds one verification key to the ring.
func (v *HttpSignatureVerifier) RegisterKey(keyID string, key KeyMaterial) error {
	return errNotImplemented
}

// RevokeKey records the instant from which a key must not sign.
func (v *HttpSignatureVerifier) RevokeKey(keyID string, at time.Time) error {
	return errNotImplemented
}

// Explain reports the rebuilt base and the checks applied to one request.
func (v *HttpSignatureVerifier) Explain(req SignedRequest) (Trace, error) {
	return Trace{}, errNotImplemented
}

// Keys lists the registered key ids in ascending order.
func (v *HttpSignatureVerifier) Keys() []string {
	return nil
}

// SigningBase rebuilds the signing base for one request without verifying it.
func (v *HttpSignatureVerifier) SigningBase(req SignedRequest) (string, error) {
	return "", errNotImplemented
}

// NonceCount reports how many nonces are held for one key id.
func (v *HttpSignatureVerifier) NonceCount(keyID string) int {
	return 0
}

// Revoked reports the revocation watermark of one key id.
func (v *HttpSignatureVerifier) Revoked(keyID string) (time.Time, bool) {
	return time.Time{}, false
}

// Snapshot serialises the key ring, revocations and nonce ledgers.
func (v *HttpSignatureVerifier) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (v *HttpSignatureVerifier) Restore(data []byte) error {
	return errNotImplemented
}
