// Package task authorises NFS export requests and maps remote identities to
// local uids and gids according to the export options.
package task

import (
	"errors"
	"net/netip"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Security flavours accepted by an export.
const (
	SecSys  = "sys"
	SecKrb5 = "krb5"
)

// Credential methods carried on a request.
const (
	MethodSys  = "sys"
	MethodKrb5 = "krb5"
)

// DefaultAnonymousUID and DefaultAnonymousGID are used when an export does not
// configure its own anonymous identity.
const (
	DefaultAnonymousUID = 65534
	DefaultAnonymousGID = 65534
)

// Errors returned by the guard.
var (
	ErrBadPath           = errors.New("export path is not a clean absolute path")
	ErrPathEscapes       = errors.New("requested path escapes the export root")
	ErrBadClient         = errors.New("client entry is malformed")
	ErrNoClients         = errors.New("export needs at least one client entry")
	ErrClientLimit       = errors.New("client entry limit exceeded for this export")
	ErrExportLimit       = errors.New("export table limit exceeded")
	ErrConflictingSquash = errors.New("squash options conflict")
	ErrBadSec            = errors.New("unknown security flavour")
	ErrNoExport          = errors.New("export not found")
	ErrClientDenied      = errors.New("client is not allowed by the export")
	ErrBadCredential     = errors.New("credential is malformed")
	ErrIdentityUnmapped  = errors.New("identity has no mapping on this export")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// PrincipalMap maps one Kerberos principal to a local identity.
type PrincipalMap struct {
	Principal string   `json:"principal"`
	UID       uint32   `json:"uid"`
	GIDs      []uint32 `json:"gids"`
}

// ExportSpec is the complete option set of one export.
type ExportSpec struct {
	Clients      []string       `json:"clients"`
	ReadOnly     bool           `json:"read_only"`
	RootSquash   bool           `json:"root_squash"`
	AllSquash    bool           `json:"all_squash"`
	NoRootSquash bool           `json:"no_root_squash"`
	NoRootHosts  []string       `json:"no_root_hosts"`
	Sec          string         `json:"sec"`
	AnonymousUID uint32         `json:"anonymous_uid"`
	AnonymousGID uint32         `json:"anonymous_gid"`
	Principals   []PrincipalMap `json:"principals"`
}

// Export is one entry of the export table.
type Export struct {
	Path    string     `json:"path"`
	Spec    ExportSpec `json:"spec"`
	Version int64      `json:"version"`
}

// Credential is the remote identity of one request.
type Credential struct {
	Method    string     `json:"method"`
	User      string     `json:"user"`
	UID       uint32     `json:"uid"`
	GIDs      []uint32   `json:"gids"`
	Principal string     `json:"principal"`
	Client    netip.Addr `json:"client"`
}

// MappedID is the local identity a request runs as.
type MappedID struct {
	Export    string   `json:"export"`
	Version   int64    `json:"version"`
	UID       uint32   `json:"uid"`
	GIDs      []uint32 `json:"gids"`
	Anonymous bool     `json:"anonymous"`
	Method    string   `json:"method"`
}

// ExportPolicy bounds the export table, its client lists and the identity
// cache, and injects the optional host and group lookups.
type ExportPolicy struct {
	MaxExports          int
	MaxClientsPerExport int
	MaxIdentityCache    int
	ResolveHost         func(name string) []string
	GroupSource         func(user string) []uint32
}

// Stats is a copy of the guard counters.
type Stats struct {
	Exports    int `json:"exports"`
	Authorized int `json:"authorized"`
	Denied     int `json:"denied"`
	Mapped     int `json:"mapped"`
	Cache      int `json:"cache"`
}

// NfsExportGuard owns the export table and the identity mapping cache.
type NfsExportGuard struct {
	policy ExportPolicy
}

// NewNfsExportGuard builds a guard with the given policy.
func NewNfsExportGuard(cfg ExportPolicy) *NfsExportGuard {
	return &NfsExportGuard{policy: cfg}
}

// AddExport inserts or replaces one export entry.
func (g *NfsExportGuard) AddExport(path string, spec ExportSpec) error {
	return errNotImplemented
}

// Authorize resolves the export that serves the client and path.
func (g *NfsExportGuard) Authorize(client netip.Addr, path string) (Export, error) {
	return Export{}, errNotImplemented
}

// MapIdentity maps a credential to the local identity on one export.
func (g *NfsExportGuard) MapIdentity(export string, cred Credential) (MappedID, error) {
	return MappedID{}, errNotImplemented
}

// Revoke removes one export entry.
func (g *NfsExportGuard) Revoke(path string) error {
	return errNotImplemented
}

// Exports returns a copy of the export table ordered by path.
func (g *NfsExportGuard) Exports() []Export {
	return nil
}

// Get reads one export entry.
func (g *NfsExportGuard) Get(path string) (Export, bool) {
	return Export{}, false
}

// Stats returns the live counters.
func (g *NfsExportGuard) Stats() Stats {
	return Stats{}
}

// Snapshot serialises exports, versions and counters.
func (g *NfsExportGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *NfsExportGuard) Restore(data []byte) error {
	return errNotImplemented
}
