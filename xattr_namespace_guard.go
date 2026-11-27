// Package task governs extended attribute namespaces: who may write into the
// user, trusted, security and system ACL namespaces, how an ACL stays in step
// with the permission bits, and what a copy across filesystems keeps.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Namespaces reported back on every attribute state.
const (
	NSUser     = "user"
	NSTrusted  = "trusted"
	NSSecurity = "security"
	NSSystem   = "system"
)

// Attribute names that carry ACL entries.
const (
	AttrACLAccess  = "system.posix_acl_access"
	AttrACLDefault = "system.posix_acl_default"
)

// Actions recorded by Copy.
const (
	ActionCopied  = "copied"
	ActionDropped = "dropped"
	ActionSkipped = "skipped"
)

// Reasons recorded by Copy.
const (
	ReasonUnsupported = "unsupported_namespace"
	ReasonReadOnlyFS  = "read_only_filesystem"
	ReasonSameFile    = "same_file"
)

// Errors returned by the guard.
var (
	ErrBlankPath     = errors.New("path is required")
	ErrPathExists    = errors.New("path is already registered")
	ErrUnknownPath   = errors.New("path is not registered")
	ErrNoFilesystem  = errors.New("no filesystem covers the path")
	ErrBadNamespace  = errors.New("attribute name has no known namespace")
	ErrPermission    = errors.New("credential is not allowed for this namespace")
	ErrAttrNotFound  = errors.New("attribute is not set")
	ErrValueTooLarge = errors.New("attribute value exceeds the limit")
	ErrAttrLimit     = errors.New("attribute count limit reached")
	ErrBadACLValue   = errors.New("acl value is malformed")
	ErrReadOnlyFS    = errors.New("filesystem is mounted read only")
	ErrBadMode       = errors.New("file mode is out of range")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// GuardConfig bounds one attribute value and the attribute set of one file.
type GuardConfig struct {
	MaxValueBytes   int
	MaxAttrsPerFile int
}

// Cred is the caller identity used for namespace admission.
type Cred struct {
	UID        int
	Privileged bool
	Trusted    bool
}

// Attribute is one name value pair with its own revision.
type Attribute struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Version int    `json:"version"`
}

// AttrState is the outcome of a write.
type AttrState struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Version     int    `json:"version"`
	Bytes       int    `json:"bytes"`
	FileVersion int    `json:"file_version"`
}

// AttrChange is one entry of a copy report.
type AttrChange struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	Version int    `json:"version"`
}

// CopyMode carries the copy flags.
type CopyMode struct {
	FollowLinks   bool
	PreserveOwner bool
}

// FSOptions declares what one mounted filesystem can store.
type FSOptions struct {
	Type             string
	SupportsTrusted  bool
	SupportsSecurity bool
	SupportsACL      bool
	ReadOnly         bool
}

// FileStats is the metadata view of one file.
type FileStats struct {
	Path    string `json:"path"`
	Mode    uint32 `json:"mode"`
	Owner   int    `json:"owner"`
	Version int    `json:"version"`
	Attrs   int    `json:"attrs"`
}

// fsMount is one registered filesystem root.
type fsMount struct {
	Path    string    `json:"path"`
	Options FSOptions `json:"options"`
}

// fileRecord is one file and its attribute set.
type fileRecord struct {
	Path    string                `json:"path"`
	Mode    uint32                `json:"mode"`
	Owner   int                   `json:"owner"`
	Version int                   `json:"version"`
	Attrs   map[string]*Attribute `json:"attrs"`
}

// XattrNamespaceGuard owns the mount table, the file table and the ACL view.
type XattrNamespaceGuard struct {
	mu     sync.Mutex
	cfg    GuardConfig
	mounts []fsMount
	files  map[string]*fileRecord
}

// NewXattrNamespaceGuard builds a guard with the given limits.
func NewXattrNamespaceGuard(cfg GuardConfig) *XattrNamespaceGuard {
	return &XattrNamespaceGuard{cfg: cfg}
}

// Mount registers a filesystem root and what it can store.
func (g *XattrNamespaceGuard) Mount(path string, opts FSOptions) error {
	return errNotImplemented
}

// Create registers a file with its initial mode and owner.
func (g *XattrNamespaceGuard) Create(path string, mode uint32, owner int) error {
	return errNotImplemented
}

// Set writes one attribute after namespace and permission admission.
func (g *XattrNamespaceGuard) Set(path string, attr Attribute, cred Cred) (AttrState, error) {
	return AttrState{}, errNotImplemented
}

// Get reads one attribute the caller is allowed to see.
func (g *XattrNamespaceGuard) Get(path, name string, cred Cred) (Attribute, error) {
	return Attribute{}, errNotImplemented
}

// List returns the attribute names visible to the caller, sorted.
func (g *XattrNamespaceGuard) List(path string, cred Cred) ([]string, error) {
	return nil, errNotImplemented
}

// Remove deletes one attribute.
func (g *XattrNamespaceGuard) Remove(path, name string, cred Cred) error {
	return errNotImplemented
}

// Copy carries the attribute set of one file onto another.
func (g *XattrNamespaceGuard) Copy(src, dst string, mode CopyMode) ([]AttrChange, error) {
	return nil, errNotImplemented
}

// Chmod rewrites the mode and keeps the ACL mask entry in step.
func (g *XattrNamespaceGuard) Chmod(path string, mode uint32, cred Cred) error {
	return errNotImplemented
}

// Capabilities derives the capability set stored in security.capability.
func (g *XattrNamespaceGuard) Capabilities(path string) []string {
	return nil
}

// FileVersion reads the revision of one file.
func (g *XattrNamespaceGuard) FileVersion(path string) (int, error) {
	return 0, errNotImplemented
}

// Attrs returns a copy of the whole attribute set of one file.
func (g *XattrNamespaceGuard) Attrs(path string) []Attribute {
	return nil
}

// FileStats reads the metadata view of one file.
func (g *XattrNamespaceGuard) FileStats(path string) (FileStats, error) {
	return FileStats{}, errNotImplemented
}

// Snapshot serialises the mount table, the file table and the revisions.
func (g *XattrNamespaceGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *XattrNamespaceGuard) Restore(data []byte) error {
	return errNotImplemented
}
