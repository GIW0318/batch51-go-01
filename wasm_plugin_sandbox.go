// Package task hosts third party WebAssembly plugins inside a sandbox that
// only exposes what the plugin manifest declares: preopened directories,
// imported host functions and environment variables.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Plugin lifecycle states reported by Stats and Plugins.
const (
	PluginLoaded  = "loaded"
	PluginActive  = "active"
	PluginFailed  = "failed"
	PluginRevoked = "revoked"
)

// Sandbox errors.
var (
	ErrInvalidPlugin      = errors.New("plugin name and code are required")
	ErrDuplicatePlugin    = errors.New("plugin name is already loaded")
	ErrPluginLimit        = errors.New("plugin limit reached")
	ErrUnknownPlugin      = errors.New("unknown plugin")
	ErrInstanceLimit      = errors.New("instance limit reached for plugin")
	ErrUnknownInstance    = errors.New("unknown instance")
	ErrPluginRevoked      = errors.New("plugin is revoked")
	ErrInstanceUnusable   = errors.New("instance is not usable")
	ErrHostFunctionDenied = errors.New("host function is not declared by the manifest")
	ErrPathDenied         = errors.New("path is not covered by a declared preopen")
	ErrPathEscape         = errors.New("path escapes the sandbox root")
	ErrFuelExhausted      = errors.New("fuel budget exhausted")
	ErrMemoryLimit        = errors.New("memory limit exceeded")
	ErrNoRunner           = errors.New("no executor is configured")
	ErrRunnerFailed       = errors.New("executor failed")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// PluginID identifies one loaded plugin inside a sandbox.
type PluginID string

// Preopen is a host directory the manifest hands to the plugin.
type Preopen struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
}

// Manifest is the capability declaration a plugin ships with.
type Manifest struct {
	Name          string    `json:"name"`
	Entry         string    `json:"entry"`
	Preopens      []Preopen `json:"preopens"`
	HostFunctions []string  `json:"host_functions"`
	Env           []string  `json:"env"`
}

// RunResult is what the injected executor reports back for one call.
type RunResult struct {
	Output   []byte `json:"output"`
	FuelUsed uint64 `json:"fuel_used"`
}

// Runner executes a plugin function with a hard fuel budget. It is injected
// by the host so the sandbox itself never interprets plugin bytecode.
type Runner interface {
	Run(fn string, args []byte, fuel uint64) (RunResult, error)
}

// SandboxConfig bounds one sandbox.
type SandboxConfig struct {
	RootDir               string
	MaxPlugins            int
	MaxInstancesPerPlugin int
	FuelPerCall           uint64
	MaxMemoryBytes        int
	MaxSnapshotBytes      int
	Runner                Runner
}

// Instance is one running copy of a plugin.
type Instance struct {
	ID       string   `json:"id"`
	PluginID PluginID `json:"plugin_id"`
}

// Usage is the per plugin accounting snapshot.
type Usage struct {
	Plugin     PluginID `json:"plugin"`
	State      string   `json:"state"`
	Instances  int      `json:"instances"`
	Calls      int64    `json:"calls"`
	FuelUsed   uint64   `json:"fuel_used"`
	MemoryPeak int      `json:"memory_peak"`
}

// PluginInfo is the list entry returned by Plugins.
type PluginInfo struct {
	ID        PluginID `json:"id"`
	Name      string   `json:"name"`
	State     string   `json:"state"`
	Instances int      `json:"instances"`
}

type pluginState struct {
	id            PluginID
	name          string
	code          []byte
	manifest      Manifest
	hostFunctions map[string]struct{}
	state         string
	calls         int64
	fuelUsed      uint64
	memoryPeak    int
	liveInstances map[string]struct{}
	instanceIDs   map[string]struct{}
}

type instanceState struct {
	id       string
	pluginID PluginID
	live     bool
	preopens []Preopen
}

// WasmPluginSandbox owns the plugin table, the instance table and the counters.
// A single lock guards every mutation so quota checks, instance creation and
// accounting updates are observed as one atomic state transition.
type WasmPluginSandbox struct {
	cfg SandboxConfig

	mu           sync.RWMutex
	rootAbs      string
	rootResolved string
	version      uint64

	plugins   map[PluginID]*pluginState
	names     map[string]PluginID
	instances map[string]*instanceState

	nextPluginSequence   uint64
	nextInstanceSequence uint64
}

// NewWasmPluginSandbox builds a sandbox with the given caps.
func NewWasmPluginSandbox(cfg SandboxConfig) *WasmPluginSandbox {
	rootAbs, err := filepath.Abs(cfg.RootDir)
	if err != nil {
		rootAbs = cfg.RootDir
	}
	rootAbs = filepath.Clean(rootAbs)
	rootResolved, err := resolveRealPath(rootAbs)
	if err != nil {
		rootResolved = rootAbs
	}
	return &WasmPluginSandbox{
		cfg:          cfg,
		rootAbs:      rootAbs,
		rootResolved: rootResolved,
		plugins:      make(map[PluginID]*pluginState),
		names:        make(map[string]PluginID),
		instances:    make(map[string]*instanceState),
	}
}

// Load registers plugin code together with its capability manifest.
func (s *WasmPluginSandbox) Load(name string, code []byte, manifest Manifest) (PluginID, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(code) == 0 {
		return "", ErrInvalidPlugin
	}

	codeCopy := append([]byte(nil), code...)
	manifestCopy := cloneManifest(manifest)
	hostFunctions := make(map[string]struct{}, len(manifestCopy.HostFunctions))
	for _, fn := range manifestCopy.HostFunctions {
		hostFunctions[fn] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.names[name]; exists {
		return "", ErrDuplicatePlugin
	}
	if len(s.plugins) >= s.cfg.MaxPlugins {
		return "", ErrPluginLimit
	}

	id := s.nextPluginIDLocked()
	s.plugins[id] = &pluginState{
		id:            id,
		name:          name,
		code:          codeCopy,
		manifest:      manifestCopy,
		hostFunctions: hostFunctions,
		state:         PluginLoaded,
		liveInstances: make(map[string]struct{}),
		instanceIDs:   make(map[string]struct{}),
	}
	s.names[name] = id
	return id, nil
}

// Instantiate creates one running copy of a loaded plugin.
func (s *WasmPluginSandbox) Instantiate(pluginID PluginID) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	plugin, ok := s.plugins[pluginID]
	if !ok {
		return Instance{}, ErrUnknownPlugin
	}
	if plugin.state == PluginRevoked {
		return Instance{}, ErrPluginRevoked
	}
	if len(plugin.liveInstances) >= s.cfg.MaxInstancesPerPlugin {
		return Instance{}, ErrInstanceLimit
	}

	preopens, err := s.resolvePreopensLocked(plugin.manifest.Preopens)
	if err != nil {
		return Instance{}, err
	}

	id := s.nextInstanceIDLocked()
	s.instances[id] = &instanceState{
		id:       id,
		pluginID: pluginID,
		live:     true,
		preopens: preopens,
	}
	plugin.liveInstances[id] = struct{}{}
	plugin.instanceIDs[id] = struct{}{}
	// A fresh instance is the only way out of a fuel-failed state. The
	// failed instance itself stays tombstoned and unusable.
	plugin.state = PluginActive
	return Instance{ID: id, PluginID: pluginID}, nil
}

// Call runs a declared host function inside one instance.
func (s *WasmPluginSandbox) Call(inst Instance, fn string, args []byte) ([]byte, error) {
	s.mu.Lock()
	plugin, ok := s.plugins[inst.PluginID]
	if !ok {
		s.mu.Unlock()
		return nil, ErrUnknownInstance
	}
	if plugin.state == PluginRevoked {
		s.mu.Unlock()
		return nil, ErrPluginRevoked
	}
	instance, ok := s.instances[inst.ID]
	if !ok || instance.pluginID != inst.PluginID {
		s.mu.Unlock()
		return nil, ErrUnknownInstance
	}
	if !instance.live {
		s.mu.Unlock()
		return nil, ErrInstanceUnusable
	}
	if _, declared := plugin.hostFunctions[fn]; !declared {
		s.mu.Unlock()
		return nil, ErrHostFunctionDenied
	}
	if len(args) > s.cfg.MaxMemoryBytes {
		s.mu.Unlock()
		return nil, ErrMemoryLimit
	}
	runner := s.cfg.Runner
	if runner == nil {
		s.mu.Unlock()
		return nil, ErrNoRunner
	}
	version := s.version
	s.mu.Unlock()

	result, runErr := runner.Run(fn, args, s.cfg.FuelPerCall)

	s.mu.Lock()
	defer s.mu.Unlock()

	// A restore replaces the whole world. A call that started before that
	// restore must not leak accounting into the restored snapshot.
	if s.version != version {
		if runErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrRunnerFailed, runErr)
		}
		return append([]byte(nil), result.Output...), nil
	}
	plugin, ok = s.plugins[inst.PluginID]
	if !ok {
		if runErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrRunnerFailed, runErr)
		}
		return append([]byte(nil), result.Output...), nil
	}

	plugin.calls++
	plugin.fuelUsed += result.FuelUsed
	if len(args) > plugin.memoryPeak {
		plugin.memoryPeak = len(args)
	}

	if runErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrRunnerFailed, runErr)
	}

	if result.FuelUsed > s.cfg.FuelPerCall {
		if instance, ok := s.instances[inst.ID]; ok && instance.pluginID == inst.PluginID {
			instance.live = false
		}
		delete(plugin.liveInstances, inst.ID)
		if plugin.state != PluginRevoked {
			plugin.state = PluginFailed
		}
		return nil, ErrFuelExhausted
	}

	if plugin.state == PluginLoaded {
		plugin.state = PluginActive
	}
	return append([]byte(nil), result.Output...), nil
}

// Revoke fences a plugin off and drops its instances.
func (s *WasmPluginSandbox) Revoke(pluginID PluginID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	plugin, ok := s.plugins[pluginID]
	if !ok {
		return ErrUnknownPlugin
	}
	if plugin.state == PluginRevoked {
		return ErrPluginRevoked
	}
	plugin.state = PluginRevoked
	for id := range plugin.instanceIDs {
		delete(s.instances, id)
	}
	plugin.instanceIDs = make(map[string]struct{})
	plugin.liveInstances = make(map[string]struct{})
	return nil
}

// Stats reads the accounting record of one plugin.
func (s *WasmPluginSandbox) Stats(pluginID PluginID) (Usage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	plugin, ok := s.plugins[pluginID]
	if !ok {
		return Usage{}, ErrUnknownPlugin
	}
	return Usage{
		Plugin:     plugin.id,
		State:      plugin.state,
		Instances:  len(plugin.liveInstances),
		Calls:      plugin.calls,
		FuelUsed:   plugin.fuelUsed,
		MemoryPeak: plugin.memoryPeak,
	}, nil
}

// Plugins lists the loaded plugins ordered by name.
func (s *WasmPluginSandbox) Plugins() []PluginInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]PluginInfo, 0, len(s.plugins))
	for _, plugin := range s.plugins {
		result = append(result, PluginInfo{
			ID:        plugin.id,
			Name:      plugin.name,
			State:     plugin.state,
			Instances: len(plugin.liveInstances),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// Instances counts the live instances of one plugin.
func (s *WasmPluginSandbox) Instances(pluginID PluginID) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	plugin, ok := s.plugins[pluginID]
	if !ok {
		return 0
	}
	return len(plugin.liveInstances)
}

// CheckPath reports whether a plugin requested path is reachable through one
// of the preopened directories declared by its manifest.
func (s *WasmPluginSandbox) CheckPath(inst Instance, path string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	plugin, ok := s.plugins[inst.PluginID]
	if !ok {
		return ErrUnknownInstance
	}
	if plugin.state == PluginRevoked {
		return ErrPluginRevoked
	}
	instance, ok := s.instances[inst.ID]
	if !ok || instance.pluginID != inst.PluginID {
		return ErrUnknownInstance
	}
	if !instance.live {
		return ErrInstanceUnusable
	}

	resolved, err := s.resolveRequestedPath(path)
	if err != nil {
		return ErrPathDenied
	}
	if !pathWithin(s.rootResolved, resolved) {
		return ErrPathEscape
	}
	for _, preopen := range instance.preopens {
		if pathWithin(preopen.Path, resolved) {
			return nil
		}
	}
	return ErrPathDenied
}

type snapshotPayload struct {
	Version              int                `json:"version"`
	RootDir              string             `json:"root_dir"`
	ResolvedRootDir      string             `json:"resolved_root_dir"`
	NextPluginSequence   uint64             `json:"next_plugin_sequence"`
	NextInstanceSequence uint64             `json:"next_instance_sequence"`
	Plugins              []snapshotPlugin   `json:"plugins"`
	Instances            []snapshotInstance `json:"instances"`
}

type snapshotPlugin struct {
	ID         PluginID `json:"id"`
	Name       string   `json:"name"`
	Code       []byte   `json:"code"`
	Manifest   Manifest `json:"manifest"`
	State      string   `json:"state"`
	Calls      int64    `json:"calls"`
	FuelUsed   uint64   `json:"fuel_used"`
	MemoryPeak int      `json:"memory_peak"`
}

type snapshotInstance struct {
	ID       string    `json:"id"`
	PluginID PluginID  `json:"plugin_id"`
	Live     bool      `json:"live"`
	Preopens []Preopen `json:"preopens"`
}

const snapshotVersion = 1

// Snapshot serialises the plugin table, the instance table and the counters.
func (s *WasmPluginSandbox) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	payload := snapshotPayload{
		Version:              snapshotVersion,
		RootDir:              s.rootAbs,
		ResolvedRootDir:      s.rootResolved,
		NextPluginSequence:   s.nextPluginSequence,
		NextInstanceSequence: s.nextInstanceSequence,
		Plugins:              make([]snapshotPlugin, 0, len(s.plugins)),
		Instances:            make([]snapshotInstance, 0, len(s.instances)),
	}
	for _, plugin := range s.plugins {
		payload.Plugins = append(payload.Plugins, snapshotPlugin{
			ID:         plugin.id,
			Name:       plugin.name,
			Code:       append([]byte(nil), plugin.code...),
			Manifest:   cloneManifest(plugin.manifest),
			State:      plugin.state,
			Calls:      plugin.calls,
			FuelUsed:   plugin.fuelUsed,
			MemoryPeak: plugin.memoryPeak,
		})
	}
	sort.Slice(payload.Plugins, func(i, j int) bool {
		if payload.Plugins[i].Name == payload.Plugins[j].Name {
			return payload.Plugins[i].ID < payload.Plugins[j].ID
		}
		return payload.Plugins[i].Name < payload.Plugins[j].Name
	})

	for _, instance := range s.instances {
		payload.Instances = append(payload.Instances, snapshotInstance{
			ID:       instance.id,
			PluginID: instance.pluginID,
			Live:     instance.live,
			Preopens: clonePreopens(instance.preopens),
		})
	}
	sort.Slice(payload.Instances, func(i, j int) bool {
		if payload.Instances[i].PluginID == payload.Instances[j].PluginID {
			return payload.Instances[i].ID < payload.Instances[j].ID
		}
		return payload.Instances[i].PluginID < payload.Instances[j].PluginID
	})

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if len(data) > s.cfg.MaxSnapshotBytes {
		return nil, fmt.Errorf("%w: encoded snapshot is %d bytes, limit is %d", ErrMemoryLimit, len(data), s.cfg.MaxSnapshotBytes)
	}
	return data, nil
}

// Restore replaces the whole sandbox state with a snapshot, all or nothing.
func (s *WasmPluginSandbox) Restore(data []byte) error {
	if len(data) == 0 || len(data) > s.cfg.MaxSnapshotBytes {
		return ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if len(payload.Plugins) > s.cfg.MaxPlugins {
		return ErrBadSnapshot
	}

	plugins := make(map[PluginID]*pluginState, len(payload.Plugins))
	names := make(map[string]PluginID, len(payload.Plugins))
	for _, saved := range payload.Plugins {
		name := strings.TrimSpace(saved.Name)
		if saved.ID == "" || name == "" || len(saved.Code) == 0 || saved.Calls < 0 || saved.MemoryPeak < 0 {
			return ErrBadSnapshot
		}
		if !validPluginState(saved.State) {
			return ErrBadSnapshot
		}
		if _, exists := plugins[saved.ID]; exists {
			return ErrBadSnapshot
		}
		if _, exists := names[name]; exists {
			return ErrBadSnapshot
		}
		manifest := cloneManifest(saved.Manifest)
		rebaseManifestPreopens(&manifest, payload.RootDir, s.rootAbs)
		hostFunctions := make(map[string]struct{}, len(manifest.HostFunctions))
		for _, fn := range manifest.HostFunctions {
			hostFunctions[fn] = struct{}{}
		}
		plugins[saved.ID] = &pluginState{
			id:            saved.ID,
			name:          name,
			code:          append([]byte(nil), saved.Code...),
			manifest:      manifest,
			hostFunctions: hostFunctions,
			state:         saved.State,
			calls:         saved.Calls,
			fuelUsed:      saved.FuelUsed,
			memoryPeak:    saved.MemoryPeak,
			liveInstances: make(map[string]struct{}),
			instanceIDs:   make(map[string]struct{}),
		}
		names[name] = saved.ID
	}

	instances := make(map[string]*instanceState, len(payload.Instances))
	for _, saved := range payload.Instances {
		if saved.ID == "" {
			return ErrBadSnapshot
		}
		plugin, exists := plugins[saved.PluginID]
		if !exists || plugin.state == PluginRevoked {
			return ErrBadSnapshot
		}
		if _, exists := instances[saved.ID]; exists {
			return ErrBadSnapshot
		}
		preopens := clonePreopens(saved.Preopens)
		rebasePreopens(preopens, payload.ResolvedRootDir, s.rootResolved)
		instances[saved.ID] = &instanceState{
			id:       saved.ID,
			pluginID: saved.PluginID,
			live:     saved.Live,
			preopens: preopens,
		}
		plugin.instanceIDs[saved.ID] = struct{}{}
		if saved.Live {
			plugin.liveInstances[saved.ID] = struct{}{}
			if len(plugin.liveInstances) > s.cfg.MaxInstancesPerPlugin {
				return ErrBadSnapshot
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.plugins = plugins
	s.names = names
	s.instances = instances
	s.nextPluginSequence = payload.NextPluginSequence
	s.nextInstanceSequence = payload.NextInstanceSequence
	s.version++
	return nil
}

func (s *WasmPluginSandbox) nextPluginIDLocked() PluginID {
	for {
		s.nextPluginSequence++
		id := PluginID(fmt.Sprintf("plugin-%d", s.nextPluginSequence))
		if _, exists := s.plugins[id]; !exists {
			return id
		}
	}
}

func (s *WasmPluginSandbox) nextInstanceIDLocked() string {
	for {
		s.nextInstanceSequence++
		id := fmt.Sprintf("instance-%d", s.nextInstanceSequence)
		if _, exists := s.instances[id]; !exists {
			return id
		}
	}
}

func (s *WasmPluginSandbox) resolvePreopensLocked(preopens []Preopen) ([]Preopen, error) {
	resolved := make([]Preopen, 0, len(preopens))
	for _, preopen := range preopens {
		if strings.TrimSpace(preopen.Path) == "" {
			return nil, ErrPathDenied
		}
		path, err := s.resolveRequestedPath(preopen.Path)
		if err != nil {
			return nil, ErrPathDenied
		}
		if !pathWithin(s.rootResolved, path) {
			return nil, ErrPathEscape
		}
		resolved = append(resolved, Preopen{Path: path, ReadOnly: preopen.ReadOnly})
	}
	return resolved, nil
}

func (s *WasmPluginSandbox) resolveRequestedPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = joinPathRaw(s.rootAbs, path)
	}
	return resolveRealPath(path)
}

// resolveRealPath resolves symlinks component by component. It also works for
// a path whose final components do not exist yet by resolving the deepest
// existing prefix and appending the remaining lexical components. Components
// are deliberately not cleaned before symlink resolution: "link/.." follows
// the link target first, exactly like the operating system does.
func resolveRealPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = joinPathRaw(workingDirectory, path)
	}

	const maxSymlinkHops = 255
	separator := string(filepath.Separator)
	root := separator
	if volume := filepath.VolumeName(path); volume != "" {
		root = volume + separator
	}

	current := root
	rest := strings.Split(strings.TrimPrefix(path, root), separator)
	hops := 0
	for len(rest) > 0 {
		part := rest[0]
		rest = rest[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}

		next := filepath.Join(current, part)
		info, err := os.Lstat(next)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
			current = next
			for _, remaining := range rest {
				switch remaining {
				case "", ".":
				case "..":
					current = filepath.Dir(current)
				default:
					current = filepath.Join(current, remaining)
				}
			}
			return filepath.Clean(current), nil
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}

		hops++
		if hops > maxSymlinkHops {
			return "", fmt.Errorf("too many symbolic links")
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		targetParts := strings.Split(target, separator)
		if filepath.IsAbs(target) {
			current = root
			targetParts = strings.Split(strings.TrimPrefix(target, root), separator)
		}
		rest = append(targetParts, rest...)
	}
	return filepath.Clean(current), nil
}

func joinPathRaw(base, relative string) string {
	if strings.HasSuffix(base, string(filepath.Separator)) {
		return base + relative
	}
	return base + string(filepath.Separator) + relative
}

func pathWithin(base, target string) bool {
	relative, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func rebaseManifestPreopens(manifest *Manifest, oldRoot, newRoot string) {
	for i := range manifest.Preopens {
		if rebased, ok := rebasePath(oldRoot, newRoot, manifest.Preopens[i].Path); ok {
			manifest.Preopens[i].Path = rebased
		}
	}
}

func rebasePreopens(preopens []Preopen, oldRoot, newRoot string) {
	for i := range preopens {
		if rebased, ok := rebasePath(oldRoot, newRoot, preopens[i].Path); ok {
			preopens[i].Path = rebased
		}
	}
}

func rebasePath(oldRoot, newRoot, path string) (string, bool) {
	if oldRoot == "" || newRoot == "" || !filepath.IsAbs(path) {
		return "", false
	}
	oldRoot = filepath.Clean(oldRoot)
	path = filepath.Clean(path)
	if !pathWithin(oldRoot, path) {
		return "", false
	}
	relative, err := filepath.Rel(oldRoot, path)
	if err != nil {
		return "", false
	}
	if relative == "." {
		return newRoot, true
	}
	return joinPathRaw(newRoot, relative), true
}

func cloneManifest(manifest Manifest) Manifest {
	manifest.Preopens = clonePreopens(manifest.Preopens)
	manifest.HostFunctions = append([]string(nil), manifest.HostFunctions...)
	manifest.Env = append([]string(nil), manifest.Env...)
	return manifest
}

func clonePreopens(preopens []Preopen) []Preopen {
	return append([]Preopen(nil), preopens...)
}

func validPluginState(state string) bool {
	switch state {
	case PluginLoaded, PluginActive, PluginFailed, PluginRevoked:
		return true
	default:
		return false
	}
}
