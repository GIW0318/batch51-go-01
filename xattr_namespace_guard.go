// Package task governs extended attribute namespaces: who may write into the
// user, trusted, security and system ACL namespaces, how an ACL stays in step
// with the permission bits, and what a copy across filesystems keeps.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

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
	Path       string                `json:"path"`
	Mode       uint32                `json:"mode"`
	Owner      int                   `json:"owner"`
	Version    int                   `json:"version"`
	Attrs      map[string]*Attribute `json:"attrs"`
	Symlink    bool                  `json:"symlink,omitempty"`
	LinkTarget string                `json:"link_target,omitempty"`
}

// XattrNamespaceGuard owns the mount table, the file table and the ACL view.
type XattrNamespaceGuard struct {
	mu            sync.Mutex
	cfg           GuardConfig
	mounts        []fsMount
	files         map[string]*fileRecord
	versionFloors map[string]int
}

// NewXattrNamespaceGuard builds a guard with the given limits.
func NewXattrNamespaceGuard(cfg GuardConfig) *XattrNamespaceGuard {
	return &XattrNamespaceGuard{
		cfg:           cfg,
		files:         make(map[string]*fileRecord),
		versionFloors: make(map[string]int),
	}
}

// Mount registers a filesystem root and what it can store.
func (g *XattrNamespaceGuard) Mount(rawPath string, opts FSOptions) error {
	cleanPath, err := normalizePath(rawPath)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	for _, mount := range g.mounts {
		if mount.Path == cleanPath {
			return ErrPathExists
		}
	}
	g.mounts = append(g.mounts, fsMount{Path: cleanPath, Options: opts})
	return nil
}

// Create registers a file with its initial mode and owner.
func (g *XattrNamespaceGuard) Create(rawPath string, mode uint32, owner int) error {
	cleanPath, err := normalizePath(rawPath)
	if err != nil {
		return err
	}
	if mode > 0o7777 {
		return ErrBadMode
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.files[cleanPath]; exists {
		return ErrPathExists
	}
	mount := g.mountForLocked(cleanPath)
	if mount == nil {
		return ErrNoFilesystem
	}
	symlink, target := inspectSymlink(cleanPath)
	g.files[cleanPath] = &fileRecord{
		Path:       cleanPath,
		Mode:       mode,
		Owner:      owner,
		Version:    0,
		Attrs:      make(map[string]*Attribute),
		Symlink:    symlink,
		LinkTarget: target,
	}
	return nil
}

// Set writes one attribute after namespace and permission admission.
func (g *XattrNamespaceGuard) Set(rawPath string, attr Attribute, cred Cred) (AttrState, error) {
	cleanPath, err := normalizePath(rawPath)
	if err != nil {
		return AttrState{}, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	file := g.files[cleanPath]
	if file == nil {
		return AttrState{}, ErrUnknownPath
	}
	namespace, err := namespaceForName(attr.Name)
	if err != nil {
		return AttrState{}, err
	}
	if !canWrite(cred, file, namespace) {
		return AttrState{}, ErrPermission
	}
	if len([]byte(attr.Value)) > g.cfg.MaxValueBytes {
		return AttrState{}, ErrValueTooLarge
	}

	old, exists := file.Attrs[attr.Name]
	if !exists && len(file.Attrs) >= g.cfg.MaxAttrsPerFile {
		return AttrState{}, ErrAttrLimit
	}

	var effectiveMode uint32
	hasEffectiveMode := false
	if attr.Name == AttrACLAccess {
		acl, err := parseACL(attr.Value)
		if err != nil {
			return AttrState{}, ErrBadACLValue
		}
		effectiveMode = (file.Mode &^ 0o777) | acl.effectiveMode()
		hasEffectiveMode = true
	}

	version := 1
	if exists {
		version = old.Version + 1
	}
	file.Attrs[attr.Name] = &Attribute{
		Name:    attr.Name,
		Value:   attr.Value,
		Version: version,
	}
	if hasEffectiveMode {
		file.Mode = effectiveMode
	}
	g.bumpFileVersion(file)

	return AttrState{
		Path:        file.Path,
		Name:        attr.Name,
		Namespace:   namespace,
		Version:     version,
		Bytes:       len([]byte(attr.Value)),
		FileVersion: file.Version,
	}, nil
}

// Get reads one attribute the caller is allowed to see.
func (g *XattrNamespaceGuard) Get(rawPath, name string, cred Cred) (Attribute, error) {
	cleanPath, err := normalizePath(rawPath)
	if err != nil {
		return Attribute{}, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	file := g.files[cleanPath]
	if file == nil {
		return Attribute{}, ErrUnknownPath
	}
	namespace, err := namespaceForName(name)
	if err != nil {
		return Attribute{}, err
	}
	attr := file.Attrs[name]
	if attr == nil {
		return Attribute{}, ErrAttrNotFound
	}
	if !canSee(cred, namespace) {
		return Attribute{}, ErrPermission
	}
	return cloneAttribute(attr), nil
}

// List returns the attribute names visible to the caller, sorted.
func (g *XattrNamespaceGuard) List(rawPath string, cred Cred) ([]string, error) {
	cleanPath, err := normalizePath(rawPath)
	if err != nil {
		return nil, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	file := g.files[cleanPath]
	if file == nil {
		return nil, ErrUnknownPath
	}

	names := make([]string, 0, len(file.Attrs))
	for name := range file.Attrs {
		namespace, err := namespaceForName(name)
		if err != nil || !canSee(cred, namespace) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Remove deletes one attribute.
func (g *XattrNamespaceGuard) Remove(rawPath, name string, cred Cred) error {
	cleanPath, err := normalizePath(rawPath)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	file := g.files[cleanPath]
	if file == nil {
		return ErrUnknownPath
	}
	namespace, err := namespaceForName(name)
	if err != nil {
		return err
	}
	if file.Attrs[name] == nil {
		return ErrAttrNotFound
	}
	if !canWrite(cred, file, namespace) {
		return ErrPermission
	}

	delete(file.Attrs, name)
	g.bumpFileVersion(file)
	return nil
}

// Copy carries the attribute set of one file onto another.
func (g *XattrNamespaceGuard) Copy(rawSrc, rawDst string, mode CopyMode) ([]AttrChange, error) {
	srcPath, err := normalizePath(rawSrc)
	if err != nil {
		return nil, err
	}
	dstPath, err := normalizePath(rawDst)
	if err != nil {
		return nil, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	srcPath, err = g.resolveCopyPathLocked(srcPath, mode.FollowLinks)
	if err != nil {
		return nil, err
	}
	dstPath, err = g.resolveCopyPathLocked(dstPath, mode.FollowLinks)
	if err != nil {
		return nil, err
	}

	source := g.files[srcPath]
	if source == nil {
		return nil, ErrUnknownPath
	}
	target := g.files[dstPath]
	if target == nil {
		return nil, ErrUnknownPath
	}
	targetMount := g.mountForLocked(dstPath)
	if targetMount == nil {
		return nil, ErrUnknownPath
	}
	if targetMount.Options.ReadOnly {
		return nil, ErrReadOnlyFS
	}

	names := sortedAttrNames(source.Attrs)
	if srcPath == dstPath {
		changes := make([]AttrChange, 0, len(names))
		for _, name := range names {
			changes = append(changes, AttrChange{
				Path:    target.Path,
				Name:    name,
				Action:  ActionSkipped,
				Reason:  ReasonSameFile,
				Version: source.Attrs[name].Version,
			})
		}
		return changes, nil
	}

	type plannedCopy struct {
		name        string
		namespace   string
		value       string
		supported   bool
		acl         *parsedACL
		oldVersion  int
		hasOldValue bool
	}

	planned := make([]plannedCopy, 0, len(names))
	finalAttrCount := len(target.Attrs)
	for _, name := range names {
		attr := source.Attrs[name]
		namespace, err := namespaceForName(name)
		if err != nil {
			return nil, err
		}
		entry := plannedCopy{
			name:      name,
			namespace: namespace,
			value:     attr.Value,
		}
		if !mountSupports(targetMount.Options, namespace) {
			planned = append(planned, entry)
			continue
		}
		if len([]byte(attr.Value)) > g.cfg.MaxValueBytes {
			return nil, ErrValueTooLarge
		}
		entry.supported = true
		if old := target.Attrs[name]; old != nil {
			entry.oldVersion = old.Version
			entry.hasOldValue = true
		} else {
			finalAttrCount++
		}
		if name == AttrACLAccess {
			acl, err := parseACL(attr.Value)
			if err != nil {
				return nil, ErrBadACLValue
			}
			entry.acl = acl
		}
		planned = append(planned, entry)
	}
	if finalAttrCount > g.cfg.MaxAttrsPerFile {
		return nil, ErrAttrLimit
	}

	newAttrs := cloneAttrs(target.Attrs)
	newMode := target.Mode
	newOwner := target.Owner
	newVersion := target.Version
	floor := g.versionFloors[target.Path]
	if target.Version > floor {
		floor = target.Version
	}
	bumpVersion := func() {
		newVersion++
		if newVersion <= floor {
			newVersion = floor + 1
		}
		floor = newVersion
	}
	copied := 0
	changes := make([]AttrChange, 0, len(planned))

	for _, entry := range planned {
		if !entry.supported {
			changes = append(changes, AttrChange{
				Path:   target.Path,
				Name:   entry.name,
				Action: ActionDropped,
				Reason: ReasonUnsupported,
			})
			continue
		}

		version := 1
		if entry.hasOldValue {
			version = entry.oldVersion + 1
		}
		newAttrs[entry.name] = &Attribute{
			Name:    entry.name,
			Value:   entry.value,
			Version: version,
		}
		bumpVersion()
		copied++
		if entry.name == AttrACLAccess {
			newMode = (newMode &^ 0o777) | entry.acl.effectiveMode()
		}
		changes = append(changes, AttrChange{
			Path:    target.Path,
			Name:    entry.name,
			Action:  ActionCopied,
			Version: version,
		})
	}

	if mode.PreserveOwner && target.Owner != source.Owner {
		newOwner = source.Owner
		if copied == 0 {
			bumpVersion()
		}
	}

	target.Attrs = newAttrs
	target.Mode = newMode
	target.Owner = newOwner
	target.Version = newVersion
	g.versionFloors[target.Path] = floor
	return changes, nil
}

// Chmod rewrites the mode and keeps the ACL mask entry in step.
func (g *XattrNamespaceGuard) Chmod(rawPath string, mode uint32, cred Cred) error {
	cleanPath, err := normalizePath(rawPath)
	if err != nil {
		return err
	}
	if mode > 0o7777 {
		return ErrBadMode
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	file := g.files[cleanPath]
	if file == nil {
		return ErrUnknownPath
	}
	if cred.UID != file.Owner && !cred.Privileged {
		return ErrPermission
	}

	aclAttr := file.Attrs[AttrACLAccess]
	var newACL string
	if aclAttr != nil {
		updated, err := rewriteACLMask(aclAttr.Value, mode)
		if err != nil {
			return ErrBadACLValue
		}
		newACL = updated
	}

	file.Mode = mode
	g.bumpFileVersion(file)
	if aclAttr != nil {
		aclAttr.Value = newACL
		aclAttr.Version++
	}
	return nil
}

// Capabilities derives the capability set stored in security.capability.
func (g *XattrNamespaceGuard) Capabilities(rawPath string) []string {
	cleanPath, err := normalizePath(rawPath)
	if err != nil {
		return nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	file := g.files[cleanPath]
	if file == nil {
		return nil
	}
	attr := file.Attrs["security.capability"]
	if attr == nil {
		return []string{}
	}
	return parseCapabilities(attr.Value)
}

// FileVersion reads the revision of one file.
func (g *XattrNamespaceGuard) FileVersion(rawPath string) (int, error) {
	cleanPath, err := normalizePath(rawPath)
	if err != nil {
		return 0, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	file := g.files[cleanPath]
	if file == nil {
		return 0, ErrUnknownPath
	}
	return file.Version, nil
}

// Attrs returns a copy of the whole attribute set of one file.
func (g *XattrNamespaceGuard) Attrs(rawPath string) []Attribute {
	cleanPath, err := normalizePath(rawPath)
	if err != nil {
		return nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	file := g.files[cleanPath]
	if file == nil {
		return nil
	}
	names := sortedAttrNames(file.Attrs)
	attrs := make([]Attribute, 0, len(names))
	for _, name := range names {
		attrs = append(attrs, cloneAttribute(file.Attrs[name]))
	}
	return attrs
}

// FileStats reads the metadata view of one file.
func (g *XattrNamespaceGuard) FileStats(rawPath string) (FileStats, error) {
	cleanPath, err := normalizePath(rawPath)
	if err != nil {
		return FileStats{}, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	file := g.files[cleanPath]
	if file == nil {
		return FileStats{}, ErrUnknownPath
	}
	return FileStats{
		Path:    file.Path,
		Mode:    file.Mode,
		Owner:   file.Owner,
		Version: file.Version,
		Attrs:   len(file.Attrs),
	}, nil
}

const snapshotFormat = 1

type snapshotPayload struct {
	Mounts        []fsMount      `json:"mounts"`
	Files         []*fileRecord  `json:"files"`
	VersionFloors map[string]int `json:"version_floors,omitempty"`
}

type snapshotEnvelope struct {
	Format  int             `json:"format"`
	Payload json.RawMessage `json:"payload"`
	Digest  string          `json:"digest"`
}

// Snapshot serialises the mount table, the file table and the revisions.
func (g *XattrNamespaceGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	floors := make(map[string]int, len(g.versionFloors))
	for filePath, floor := range g.versionFloors {
		floors[filePath] = floor
	}
	payload := snapshotPayload{
		Mounts:        append([]fsMount(nil), g.mounts...),
		Files:         make([]*fileRecord, 0, len(g.files)),
		VersionFloors: floors,
	}
	filePaths := make([]string, 0, len(g.files))
	for filePath := range g.files {
		filePaths = append(filePaths, filePath)
	}
	sort.Strings(filePaths)
	for _, filePath := range filePaths {
		payload.Files = append(payload.Files, cloneFile(filePath, g.files[filePath]))
	}
	g.mu.Unlock()

	sort.Slice(payload.Mounts, func(i, j int) bool {
		return payload.Mounts[i].Path < payload.Mounts[j].Path
	})
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(rawPayload)
	envelope := snapshotEnvelope{
		Format:  snapshotFormat,
		Payload: rawPayload,
		Digest:  hex.EncodeToString(sum[:]),
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *XattrNamespaceGuard) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := decodeStrict(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Format != snapshotFormat || len(envelope.Payload) == 0 || envelope.Digest == "" {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Digest != hex.EncodeToString(sum[:]) {
		return ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := decodeStrict(envelope.Payload, &payload); err != nil {
		return ErrBadSnapshot
	}

	mounts, err := validateSnapshotMounts(payload.Mounts)
	if err != nil {
		return ErrBadSnapshot
	}
	files, err := validateSnapshotFiles(payload.Files, mounts)
	if err != nil {
		return ErrBadSnapshot
	}
	floors := make(map[string]int, len(payload.VersionFloors))
	for filePath, floor := range payload.VersionFloors {
		cleanPath, err := normalizePath(filePath)
		if err != nil || floor < 0 {
			return ErrBadSnapshot
		}
		if _, duplicate := floors[cleanPath]; duplicate {
			return ErrBadSnapshot
		}
		floors[cleanPath] = floor
	}
	for filePath, file := range files {
		if file.Version > floors[filePath] {
			floors[filePath] = file.Version
		}
	}

	g.mu.Lock()
	for filePath, floor := range g.versionFloors {
		if floor > floors[filePath] {
			floors[filePath] = floor
		}
	}
	g.mounts = mounts
	g.files = files
	g.versionFloors = floors
	g.mu.Unlock()
	return nil
}

func (g *XattrNamespaceGuard) bumpFileVersion(file *fileRecord) {
	floor := g.versionFloors[file.Path]
	if file.Version > floor {
		floor = file.Version
	}
	file.Version = floor + 1
	g.versionFloors[file.Path] = file.Version
}

func normalizePath(rawPath string) (string, error) {
	if strings.TrimSpace(rawPath) == "" {
		return "", ErrBlankPath
	}
	cleanPath := path.Clean(rawPath)
	if cleanPath == "." {
		cleanPath = "/"
	}
	return cleanPath, nil
}

func (g *XattrNamespaceGuard) mountForLocked(filePath string) *fsMount {
	best := -1
	bestLen := -1
	for i := range g.mounts {
		root := g.mounts[i].Path
		if root == "/" {
			if filePath != "" && strings.HasPrefix(filePath, "/") && bestLen < len(root) {
				best = i
				bestLen = len(root)
			}
			continue
		}
		if filePath == root || strings.HasPrefix(filePath, root+"/") {
			if len(root) > bestLen {
				best = i
				bestLen = len(root)
			}
		}
	}
	if best < 0 {
		return nil
	}
	return &g.mounts[best]
}

func namespaceForName(name string) (string, error) {
	switch {
	case len(name) > len("user.") && strings.HasPrefix(name, "user."):
		return NSUser, nil
	case len(name) > len("trusted.") && strings.HasPrefix(name, "trusted."):
		return NSTrusted, nil
	case len(name) > len("security.") && strings.HasPrefix(name, "security."):
		return NSSecurity, nil
	case len(name) > len("system.posix_acl_") && strings.HasPrefix(name, "system.posix_acl_"):
		return NSSystem, nil
	default:
		return "", ErrBadNamespace
	}
}

func canWrite(cred Cred, file *fileRecord, namespace string) bool {
	switch namespace {
	case NSUser:
		return cred.UID == file.Owner
	case NSTrusted:
		return cred.Privileged
	case NSSecurity:
		return cred.Trusted
	case NSSystem:
		return cred.UID == file.Owner || cred.Privileged
	default:
		return false
	}
}

func canSee(cred Cred, namespace string) bool {
	switch namespace {
	case NSTrusted:
		return cred.Privileged
	case NSSecurity:
		return cred.Trusted
	default:
		return true
	}
}

func mountSupports(opts FSOptions, namespace string) bool {
	switch namespace {
	case NSUser:
		return true
	case NSTrusted:
		return opts.SupportsTrusted
	case NSSecurity:
		return opts.SupportsSecurity
	case NSSystem:
		return opts.SupportsACL
	default:
		return false
	}
}

type aclEntry struct {
	permissions uint32
	fieldIndex  int
}

type parsedACL struct {
	entries map[string]aclEntry
	order   []string
}

func parseACL(value string) (*parsedACL, error) {
	segments := strings.Split(value, ",")
	parsed := &parsedACL{entries: make(map[string]aclEntry)}
	for index, segment := range segments {
		if segment == "" {
			return nil, ErrBadACLValue
		}
		fields := strings.Split(segment, ":")
		if len(fields) != 3 {
			return nil, ErrBadACLValue
		}
		tag := fields[0]
		qualifier := fields[1]
		permissions, ok := parseACLPermissions(fields[2])
		if !ok {
			return nil, ErrBadACLValue
		}

		baseTag := ""
		switch tag {
		case "u", "user":
			if qualifier == "" {
				baseTag = "u"
			}
		case "g", "group":
			if qualifier == "" {
				baseTag = "g"
			}
		case "o", "other":
			if qualifier == "" {
				baseTag = "o"
			}
		case "m", "mask":
			if qualifier == "" {
				baseTag = "m"
			}
		default:
			return nil, ErrBadACLValue
		}
		if baseTag == "" {
			continue
		}
		if _, duplicate := parsed.entries[baseTag]; duplicate {
			return nil, ErrBadACLValue
		}
		parsed.entries[baseTag] = aclEntry{permissions: permissions, fieldIndex: index}
		parsed.order = append(parsed.order, baseTag)
	}

	for _, required := range []string{"u", "g", "o", "m"} {
		if _, ok := parsed.entries[required]; !ok {
			return nil, ErrBadACLValue
		}
	}
	return parsed, nil
}

func parseACLPermissions(value string) (uint32, bool) {
	if len(value) != 3 {
		return 0, false
	}
	if value[0] != 'r' && value[0] != '-' {
		return 0, false
	}
	if value[1] != 'w' && value[1] != '-' {
		return 0, false
	}
	if value[2] != 'x' && value[2] != '-' {
		return 0, false
	}
	var permissions uint32
	if value[0] == 'r' {
		permissions |= 4
	}
	if value[1] == 'w' {
		permissions |= 2
	}
	if value[2] == 'x' {
		permissions |= 1
	}
	return permissions, true
}

func (acl *parsedACL) effectiveMode() uint32 {
	user := acl.entries["u"].permissions
	group := acl.entries["m"].permissions
	other := acl.entries["o"].permissions
	return user<<6 | group<<3 | other
}

func rewriteACLMask(value string, mode uint32) (string, error) {
	acl, err := parseACL(value)
	if err != nil {
		return "", err
	}
	segments := strings.Split(value, ",")
	maskIndex := acl.entries["m"].fieldIndex
	fields := strings.Split(segments[maskIndex], ":")
	fields[2] = permissionsString((mode >> 3) & 0o7)
	segments[maskIndex] = strings.Join(fields, ":")
	return strings.Join(segments, ","), nil
}

func permissionsString(permissions uint32) string {
	out := []byte("---")
	if permissions&4 != 0 {
		out[0] = 'r'
	}
	if permissions&2 != 0 {
		out[1] = 'w'
	}
	if permissions&1 != 0 {
		out[2] = 'x'
	}
	return string(out)
}

var capabilityPattern = regexp.MustCompile(`(?i)cap_[a-z0-9_]+`)

func parseCapabilities(value string) []string {
	matches := capabilityPattern.FindAllString(value, -1)
	seen := make(map[string]struct{}, len(matches))
	capabilities := make([]string, 0, len(matches))
	for _, match := range matches {
		name := strings.ToLower(match)
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		capabilities = append(capabilities, name)
	}
	sort.Strings(capabilities)
	return capabilities
}

func sortedAttrNames(attrs map[string]*Attribute) []string {
	names := make([]string, 0, len(attrs))
	for name := range attrs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func cloneAttribute(attr *Attribute) Attribute {
	return Attribute{Name: attr.Name, Value: attr.Value, Version: attr.Version}
}

func cloneAttrs(attrs map[string]*Attribute) map[string]*Attribute {
	cloned := make(map[string]*Attribute, len(attrs))
	for name, attr := range attrs {
		copyAttr := cloneAttribute(attr)
		cloned[name] = &copyAttr
	}
	return cloned
}

func cloneFile(filePath string, file *fileRecord) *fileRecord {
	return &fileRecord{
		Path:       filePath,
		Mode:       file.Mode,
		Owner:      file.Owner,
		Version:    file.Version,
		Attrs:      cloneAttrs(file.Attrs),
		Symlink:    file.Symlink,
		LinkTarget: file.LinkTarget,
	}
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing snapshot data")
	}
	return nil
}

func validateSnapshotMounts(source []fsMount) ([]fsMount, error) {
	mounts := make([]fsMount, 0, len(source))
	seen := make(map[string]struct{}, len(source))
	for _, mount := range source {
		cleanPath, err := normalizePath(mount.Path)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[cleanPath]; duplicate {
			return nil, errors.New("duplicate mount")
		}
		seen[cleanPath] = struct{}{}
		mount.Path = cleanPath
		mounts = append(mounts, mount)
	}
	return mounts, nil
}

func validateSnapshotFiles(source []*fileRecord, mounts []fsMount) (map[string]*fileRecord, error) {
	files := make(map[string]*fileRecord, len(source))
	for _, sourceFile := range source {
		if sourceFile == nil {
			return nil, errors.New("nil file")
		}
		cleanPath, err := normalizePath(sourceFile.Path)
		if err != nil {
			return nil, err
		}
		if _, duplicate := files[cleanPath]; duplicate {
			return nil, errors.New("duplicate file")
		}
		if sourceFile.Mode > 0o7777 || sourceFile.Version < 0 {
			return nil, errors.New("invalid file metadata")
		}
		if !snapshotPathCovered(cleanPath, mounts) {
			return nil, errors.New("file has no mount")
		}

		attrs := make(map[string]*Attribute, len(sourceFile.Attrs))
		for name, attr := range sourceFile.Attrs {
			if attr == nil || name == "" || attr.Name != name || attr.Version < 1 {
				return nil, errors.New("invalid attribute")
			}
			if attr.Version > sourceFile.Version {
				return nil, errors.New("attribute revision exceeds file revision")
			}
			if _, err := namespaceForName(name); err != nil {
				return nil, err
			}
			if name == AttrACLAccess {
				if _, err := parseACL(attr.Value); err != nil {
					return nil, err
				}
			}
			copyAttr := cloneAttribute(attr)
			attrs[name] = &copyAttr
		}

		files[cleanPath] = &fileRecord{
			Path:       cleanPath,
			Mode:       sourceFile.Mode,
			Owner:      sourceFile.Owner,
			Version:    sourceFile.Version,
			Attrs:      attrs,
			Symlink:    sourceFile.Symlink,
			LinkTarget: sourceFile.LinkTarget,
		}
	}
	return files, nil
}

func snapshotPathCovered(filePath string, mounts []fsMount) bool {
	for _, mount := range mounts {
		if mount.Path == "/" {
			if strings.HasPrefix(filePath, "/") {
				return true
			}
			continue
		}
		if filePath == mount.Path || strings.HasPrefix(filePath, mount.Path+"/") {
			return true
		}
	}
	return false
}

func inspectSymlink(filePath string) (bool, string) {
	info, err := os.Lstat(filePath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false, ""
	}
	target, err := os.Readlink(filePath)
	if err != nil {
		return true, ""
	}
	return true, target
}

func pathHasSymlink(filePath string) bool {
	if filePath == "" {
		return false
	}
	absolute, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}
	volume := filepath.VolumeName(absolute)
	rest := strings.TrimPrefix(absolute, volume)
	parts := strings.Split(strings.Trim(rest, string(os.PathSeparator)), string(os.PathSeparator))
	current := volume + string(os.PathSeparator)
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

func (g *XattrNamespaceGuard) resolveCopyPathLocked(filePath string, follow bool) (string, error) {
	record := g.files[filePath]
	isLink := (record != nil && record.Symlink) || pathHasSymlink(filePath)
	if !isLink {
		return filePath, nil
	}
	if !follow {
		return "", ErrPermission
	}

	resolved := ""
	if record != nil && record.LinkTarget != "" {
		resolved = record.LinkTarget
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(filePath), resolved)
		}
	}
	if evaluated, err := filepath.EvalSymlinks(filePath); err == nil {
		resolved = evaluated
	}
	if resolved == "" {
		return "", ErrUnknownPath
	}
	resolved = path.Clean(filepath.ToSlash(resolved))
	if resolved == "." {
		resolved = "/"
	}
	return resolved, nil
}
