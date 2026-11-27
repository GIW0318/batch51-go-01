// Package task authorises NFS export requests and maps remote identities to
// local uids and gids according to the export options.
package task

import (
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
)

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

const (
	snapshotMagic   = "nfs-export-guard"
	snapshotVersion = 1
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

type compiledClients struct {
	prefixes []netip.Prefix
	addrs    []netip.Addr
}

func (c compiledClients) contains(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range c.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	for _, candidate := range c.addrs {
		if addr == candidate {
			return true
		}
	}
	return false
}

type exportRecord struct {
	path        string
	spec        ExportSpec
	version     int64
	sec         string
	clients     compiledClients
	noRootHosts compiledClients
	principals  map[string]PrincipalMap
}

func (r *exportRecord) asExport() Export {
	return Export{Path: r.path, Spec: cloneSpec(r.spec), Version: r.version}
}

type guardCounters struct {
	authorized int
	denied     int
	mapped     int
}

type identityCacheKey struct {
	export    string
	version   int64
	method    string
	user      string
	uid       uint32
	gids      string
	principal string
	client    netip.Addr
}

type identityCacheValue struct {
	key identityCacheKey
	id  MappedID
}

type snapshotCounters struct {
	Authorized int `json:"authorized"`
	Denied     int `json:"denied"`
	Mapped     int `json:"mapped"`
}

type snapshotDocument struct {
	Magic    string           `json:"magic"`
	Format   int              `json:"format"`
	Exports  []Export         `json:"exports"`
	Counters snapshotCounters `json:"counters"`
}

// NfsExportGuard owns the export table and the identity mapping cache.
type NfsExportGuard struct {
	mu       sync.RWMutex
	policy   ExportPolicy
	exports  map[string]*exportRecord
	cache    map[identityCacheKey]*list.Element
	lru      *list.List
	counters guardCounters
}

// NewNfsExportGuard builds a guard with the given policy.
func NewNfsExportGuard(cfg ExportPolicy) *NfsExportGuard {
	if cfg.MaxExports < 0 {
		cfg.MaxExports = 0
	}
	if cfg.MaxClientsPerExport < 0 {
		cfg.MaxClientsPerExport = 0
	}
	if cfg.MaxIdentityCache < 0 {
		cfg.MaxIdentityCache = 0
	}
	return &NfsExportGuard{
		policy:  cfg,
		exports: make(map[string]*exportRecord),
		cache:   make(map[identityCacheKey]*list.Element),
		lru:     list.New(),
	}
}

// AddExport inserts or replaces one export entry.
func (g *NfsExportGuard) AddExport(exportPath string, spec ExportSpec) error {
	record, err := prepareExport(exportPath, spec, 0, g.policy)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if current, ok := g.exports[exportPath]; ok {
		record.version = current.version + 1
	} else {
		if len(g.exports) >= g.policy.MaxExports {
			return ErrExportLimit
		}
		record.version = 1
	}

	g.exports[exportPath] = record
	g.dropExportCacheLocked(exportPath)
	return nil
}

// Authorize resolves the export that serves the client and path.
func (g *NfsExportGuard) Authorize(client netip.Addr, requestPath string) (Export, error) {
	cleanPath, err := cleanRequestPath(requestPath)
	if err != nil {
		return Export{}, err
	}

	g.mu.RLock()
	record := g.longestExportLocked(cleanPath)
	if record == nil {
		g.mu.RUnlock()
		return Export{}, ErrNoExport
	}
	allowed := record.clients.contains(client)
	result := record.asExport()
	g.mu.RUnlock()

	g.mu.Lock()
	if !allowed {
		g.counters.denied++
		g.mu.Unlock()
		return Export{}, ErrClientDenied
	}
	g.counters.authorized++
	g.mu.Unlock()
	return result, nil
}

// MapIdentity maps a credential to the local identity on one export.
func (g *NfsExportGuard) MapIdentity(exportPath string, cred Credential) (MappedID, error) {
	g.mu.RLock()
	record, ok := g.exports[exportPath]
	if !ok {
		g.mu.RUnlock()
		return MappedID{}, ErrNoExport
	}
	recordSnapshot := *record
	g.mu.RUnlock()

	if err := recordSnapshot.validateCredential(cred); err != nil {
		return MappedID{}, err
	}

	key := makeIdentityCacheKey(recordSnapshot, cred)
	g.mu.Lock()
	if cached, ok := g.cacheGetLocked(key); ok {
		g.counters.mapped++
		g.mu.Unlock()
		return cached, nil
	}
	g.mu.Unlock()

	mapped, err := recordSnapshot.mapCredential(cred, g.policy.GroupSource)
	if err != nil {
		return MappedID{}, err
	}

	g.mu.Lock()
	if current, ok := g.exports[exportPath]; ok && current.version == recordSnapshot.version {
		g.cachePutLocked(key, mapped)
	}
	g.counters.mapped++
	g.mu.Unlock()
	return cloneMappedID(mapped), nil
}

// Revoke removes one export entry.
func (g *NfsExportGuard) Revoke(exportPath string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.exports[exportPath]; !ok {
		return ErrNoExport
	}
	delete(g.exports, exportPath)
	g.dropExportCacheLocked(exportPath)
	return nil
}

// Exports returns a copy of the export table ordered by path.
func (g *NfsExportGuard) Exports() []Export {
	g.mu.RLock()
	records := make([]*exportRecord, 0, len(g.exports))
	for _, record := range g.exports {
		records = append(records, record)
	}
	g.mu.RUnlock()

	sort.Slice(records, func(i, j int) bool { return records[i].path < records[j].path })
	result := make([]Export, len(records))
	for i, record := range records {
		result[i] = record.asExport()
	}
	return result
}

// Get reads one export entry.
func (g *NfsExportGuard) Get(exportPath string) (Export, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	record, ok := g.exports[exportPath]
	if !ok {
		return Export{}, false
	}
	return record.asExport(), true
}

// Stats returns the live counters.
func (g *NfsExportGuard) Stats() Stats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return Stats{
		Exports:    len(g.exports),
		Authorized: g.counters.authorized,
		Denied:     g.counters.denied,
		Mapped:     g.counters.mapped,
		Cache:      len(g.cache),
	}
}

// Snapshot serialises exports, versions and counters.
func (g *NfsExportGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	document := snapshotDocument{
		Magic:   snapshotMagic,
		Format:  snapshotVersion,
		Exports: make([]Export, 0, len(g.exports)),
		Counters: snapshotCounters{
			Authorized: g.counters.authorized,
			Denied:     g.counters.denied,
			Mapped:     g.counters.mapped,
		},
	}
	for _, record := range g.exports {
		document.Exports = append(document.Exports, record.asExport())
	}
	g.mu.RUnlock()

	sort.Slice(document.Exports, func(i, j int) bool {
		return document.Exports[i].Path < document.Exports[j].Path
	})
	return json.Marshal(document)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *NfsExportGuard) Restore(data []byte) error {
	var document snapshotDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return badSnapshot(err)
	}
	if document.Magic != snapshotMagic || document.Format != snapshotVersion {
		return badSnapshot(errors.New("unsupported snapshot format"))
	}
	if document.Counters.Authorized < 0 || document.Counters.Denied < 0 || document.Counters.Mapped < 0 {
		return badSnapshot(errors.New("negative snapshot counter"))
	}
	if len(document.Exports) > g.policy.MaxExports {
		return badSnapshot(errors.New("snapshot exceeds export limit"))
	}

	restored := make(map[string]*exportRecord, len(document.Exports))
	for _, exported := range document.Exports {
		if exported.Version < 1 {
			return badSnapshot(errors.New("invalid export version"))
		}
		if _, duplicate := restored[exported.Path]; duplicate {
			return badSnapshot(errors.New("duplicate export path"))
		}
		record, err := prepareExport(exported.Path, exported.Spec, exported.Version, g.policy)
		if err != nil {
			return badSnapshot(err)
		}
		restored[exported.Path] = record
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.exports = restored
	g.cache = make(map[identityCacheKey]*list.Element)
	g.lru.Init()
	g.counters = guardCounters{
		authorized: document.Counters.Authorized,
		denied:     document.Counters.Denied,
		mapped:     document.Counters.Mapped,
	}
	return nil
}

func prepareExport(exportPath string, spec ExportSpec, version int64, policy ExportPolicy) (*exportRecord, error) {
	if _, err := cleanExportPath(exportPath); err != nil {
		return nil, err
	}
	if spec.AllSquash && spec.NoRootSquash || spec.RootSquash && spec.NoRootSquash {
		return nil, ErrConflictingSquash
	}

	security := spec.Sec
	if security == "" {
		security = SecSys
	}
	if security != SecSys && security != SecKrb5 {
		return nil, ErrBadSec
	}

	clients, err := compileClients(spec.Clients, policy.MaxClientsPerExport, policy.ResolveHost)
	if err != nil {
		return nil, err
	}
	if len(spec.Clients)+len(spec.NoRootHosts) > policy.MaxClientsPerExport {
		return nil, ErrClientLimit
	}

	var noRootHosts compiledClients
	if spec.NoRootSquash {
		if len(spec.NoRootHosts) == 0 {
			return nil, ErrNoClients
		}
		noRootHosts, err = compileClients(spec.NoRootHosts, policy.MaxClientsPerExport, policy.ResolveHost)
		if err != nil {
			return nil, err
		}
	} else if len(spec.NoRootHosts) > 0 {
		if _, err := compileClients(spec.NoRootHosts, policy.MaxClientsPerExport, policy.ResolveHost); err != nil {
			return nil, err
		}
	}

	principals := make(map[string]PrincipalMap, len(spec.Principals))
	for _, principal := range spec.Principals {
		principals[principal.Principal] = PrincipalMap{
			Principal: principal.Principal,
			UID:       principal.UID,
			GIDs:      cloneUint32s(principal.GIDs),
		}
	}

	return &exportRecord{
		path:        exportPath,
		spec:        cloneSpec(spec),
		version:     version,
		sec:         security,
		clients:     clients,
		noRootHosts: noRootHosts,
		principals:  principals,
	}, nil
}

func compileClients(entries []string, limit int, resolveHost func(string) []string) (compiledClients, error) {
	if len(entries) == 0 {
		return compiledClients{}, ErrNoClients
	}
	if len(entries) > limit {
		return compiledClients{}, ErrClientLimit
	}

	compiled := compiledClients{}
	for _, entry := range entries {
		item := strings.TrimSpace(entry)
		if item == "" {
			return compiledClients{}, ErrBadClient
		}
		if strings.Contains(item, "/") {
			prefix, err := netip.ParsePrefix(item)
			if err != nil {
				return compiledClients{}, ErrBadClient
			}
			if err := appendClientPrefix(&compiled, prefix); err != nil {
				return compiledClients{}, err
			}
			continue
		}
		if addr, err := netip.ParseAddr(item); err == nil {
			compiled.addrs = append(compiled.addrs, addr.Unmap())
			continue
		}
		if resolveHost == nil {
			return compiledClients{}, ErrBadClient
		}
		resolved := resolveHost(item)
		if len(resolved) == 0 {
			return compiledClients{}, ErrBadClient
		}
		for _, hostAddress := range resolved {
			hostAddress = strings.TrimSpace(hostAddress)
			if hostAddress == "" {
				return compiledClients{}, ErrBadClient
			}
			if strings.Contains(hostAddress, "/") {
				prefix, err := netip.ParsePrefix(hostAddress)
				if err != nil {
					return compiledClients{}, ErrBadClient
				}
				if err := appendClientPrefix(&compiled, prefix); err != nil {
					return compiledClients{}, err
				}
				continue
			}
			addr, err := netip.ParseAddr(hostAddress)
			if err != nil {
				return compiledClients{}, ErrBadClient
			}
			compiled.addrs = append(compiled.addrs, addr.Unmap())
		}
	}
	return compiled, nil
}

func appendClientPrefix(compiled *compiledClients, prefix netip.Prefix) error {
	if prefix.Addr().Is4In6() {
		bits := prefix.Bits() - 96
		if bits < 0 {
			return ErrBadClient
		}
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), bits)
	}
	compiled.prefixes = append(compiled.prefixes, prefix.Masked())
	return nil
}

func (r *exportRecord) validateCredential(cred Credential) error {
	if cred.Method != MethodSys && cred.Method != MethodKrb5 {
		return ErrBadCredential
	}
	if r.sec == SecKrb5 {
		if cred.Method != MethodKrb5 {
			return ErrIdentityUnmapped
		}
		return nil
	}
	if cred.Method != MethodSys {
		return ErrIdentityUnmapped
	}
	return nil
}

func (r *exportRecord) mapCredential(cred Credential, groupSource func(string) []uint32) (MappedID, error) {
	uid := cred.UID
	gids := cloneUint32s(cred.GIDs)
	user := cred.User

	if r.sec == SecKrb5 {
		principal, ok := r.principals[cred.Principal]
		if !ok || cred.Principal == "" {
			return MappedID{}, ErrIdentityUnmapped
		}
		uid = principal.UID
		gids = cloneUint32s(principal.GIDs)
		if user == "" {
			user = principal.Principal
		}
	}

	anonymous := false
	switch {
	case r.spec.AllSquash:
		anonymous = true
	case uid == 0 && r.spec.RootSquash:
		anonymous = true
	case uid == 0 && r.spec.NoRootSquash && !r.noRootHosts.contains(cred.Client):
		anonymous = true
	}

	if anonymous {
		return MappedID{
			Export:    r.path,
			Version:   r.version,
			UID:       anonymousUID(r.spec),
			GIDs:      []uint32{anonymousGID(r.spec)},
			Anonymous: true,
			Method:    cred.Method,
		}, nil
	}

	if groupSource != nil {
		gids = mergeGIDs(gids, groupSource(user))
	}
	return MappedID{
		Export:    r.path,
		Version:   r.version,
		UID:       uid,
		GIDs:      gids,
		Anonymous: false,
		Method:    cred.Method,
	}, nil
}

func (g *NfsExportGuard) longestExportLocked(requestPath string) *exportRecord {
	var best *exportRecord
	for exportPath, record := range g.exports {
		if exportPath == "/" || requestPath == exportPath || strings.HasPrefix(requestPath, exportPath+"/") {
			if best == nil || len(exportPath) > len(best.path) {
				best = record
			}
		}
	}
	return best
}

func (g *NfsExportGuard) dropExportCacheLocked(exportPath string) {
	for key, element := range g.cache {
		if key.export != exportPath {
			continue
		}
		delete(g.cache, key)
		g.lru.Remove(element)
	}
}

func (g *NfsExportGuard) cacheGetLocked(key identityCacheKey) (MappedID, bool) {
	element, ok := g.cache[key]
	if !ok {
		return MappedID{}, false
	}
	g.lru.MoveToFront(element)
	value := element.Value.(identityCacheValue)
	return cloneMappedID(value.id), true
}

func (g *NfsExportGuard) cachePutLocked(key identityCacheKey, id MappedID) {
	if g.policy.MaxIdentityCache <= 0 {
		return
	}
	if element, ok := g.cache[key]; ok {
		element.Value = identityCacheValue{key: key, id: cloneMappedID(id)}
		g.lru.MoveToFront(element)
		return
	}
	for len(g.cache) >= g.policy.MaxIdentityCache {
		oldest := g.lru.Back()
		if oldest == nil {
			break
		}
		value := oldest.Value.(identityCacheValue)
		delete(g.cache, value.key)
		g.lru.Remove(oldest)
	}
	element := g.lru.PushFront(identityCacheValue{key: key, id: cloneMappedID(id)})
	g.cache[key] = element
}

func makeIdentityCacheKey(record exportRecord, cred Credential) identityCacheKey {
	return identityCacheKey{
		export:    record.path,
		version:   record.version,
		method:    cred.Method,
		user:      cred.User,
		uid:       cred.UID,
		gids:      gidFingerprint(cred.GIDs),
		principal: cred.Principal,
		client:    cred.Client,
	}
}

func gidFingerprint(gids []uint32) string {
	var builder strings.Builder
	for _, gid := range gids {
		builder.WriteString(strconv.FormatUint(uint64(gid), 10))
		builder.WriteByte(',')
	}
	return builder.String()
}

func mergeGIDs(primary []uint32, extra []uint32) []uint32 {
	if len(primary) == 0 && len(extra) == 0 {
		return nil
	}
	result := make([]uint32, 0, len(primary)+len(extra))
	seen := make(map[uint32]struct{}, len(primary)+len(extra))
	for _, group := range primary {
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		result = append(result, group)
	}
	for _, group := range extra {
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		result = append(result, group)
	}
	return result
}

func anonymousUID(spec ExportSpec) uint32 {
	if spec.AnonymousUID == 0 {
		return DefaultAnonymousUID
	}
	return spec.AnonymousUID
}

func anonymousGID(spec ExportSpec) uint32 {
	if spec.AnonymousGID == 0 {
		return DefaultAnonymousGID
	}
	return spec.AnonymousGID
}

func cleanExportPath(exportPath string) (string, error) {
	if exportPath == "" || !strings.HasPrefix(exportPath, "/") {
		return "", ErrBadPath
	}
	for _, segment := range strings.Split(exportPath, "/") {
		if segment == ".." {
			return "", ErrBadPath
		}
	}
	if path.Clean(exportPath) != exportPath {
		return "", ErrBadPath
	}
	return exportPath, nil
}

func cleanRequestPath(requestPath string) (string, error) {
	if requestPath == "" || !strings.HasPrefix(requestPath, "/") {
		return "", ErrBadPath
	}
	for _, segment := range strings.Split(requestPath, "/") {
		if segment == ".." {
			return "", ErrPathEscapes
		}
	}
	return path.Clean(requestPath), nil
}

func cloneSpec(spec ExportSpec) ExportSpec {
	clone := spec
	clone.Clients = cloneStrings(spec.Clients)
	clone.NoRootHosts = cloneStrings(spec.NoRootHosts)
	if spec.Principals != nil {
		clone.Principals = make([]PrincipalMap, len(spec.Principals))
		for i, principal := range spec.Principals {
			clone.Principals[i] = PrincipalMap{
				Principal: principal.Principal,
				UID:       principal.UID,
				GIDs:      cloneUint32s(principal.GIDs),
			}
		}
	}
	return clone
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
}

func cloneUint32s(values []uint32) []uint32 {
	if values == nil {
		return nil
	}
	clone := make([]uint32, len(values))
	copy(clone, values)
	return clone
}

func cloneMappedID(id MappedID) MappedID {
	id.GIDs = cloneUint32s(id.GIDs)
	return id
}

func badSnapshot(err error) error {
	return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
}
