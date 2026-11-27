// Package task hosts third party WebAssembly plugins inside a sandbox that
// only exposes what the plugin manifest declares: preopened directories,
// imported host functions and environment variables.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// WasmPluginSandbox owns the plugin table, the instance table and the counters.
type WasmPluginSandbox struct {
	cfg SandboxConfig
}

// NewWasmPluginSandbox builds a sandbox with the given caps.
func NewWasmPluginSandbox(cfg SandboxConfig) *WasmPluginSandbox {
	return &WasmPluginSandbox{cfg: cfg}
}

// Load registers plugin code together with its capability manifest.
func (s *WasmPluginSandbox) Load(name string, code []byte, manifest Manifest) (PluginID, error) {
	return "", errNotImplemented
}

// Instantiate creates one running copy of a loaded plugin.
func (s *WasmPluginSandbox) Instantiate(pluginID PluginID) (Instance, error) {
	return Instance{}, errNotImplemented
}

// Call runs a declared host function inside one instance.
func (s *WasmPluginSandbox) Call(inst Instance, fn string, args []byte) ([]byte, error) {
	return nil, errNotImplemented
}

// Revoke fences a plugin off and drops its instances.
func (s *WasmPluginSandbox) Revoke(pluginID PluginID) error {
	return errNotImplemented
}

// Stats reads the accounting record of one plugin.
func (s *WasmPluginSandbox) Stats(pluginID PluginID) (Usage, error) {
	return Usage{}, errNotImplemented
}

// Plugins lists the loaded plugins ordered by name.
func (s *WasmPluginSandbox) Plugins() []PluginInfo {
	return nil
}

// Instances counts the live instances of one plugin.
func (s *WasmPluginSandbox) Instances(pluginID PluginID) int {
	return 0
}

// CheckPath reports whether a plugin requested path is reachable through one
// of the preopened directories declared by its manifest.
func (s *WasmPluginSandbox) CheckPath(inst Instance, path string) error {
	return errNotImplemented
}

// Snapshot serialises the plugin table, the instance table and the counters.
func (s *WasmPluginSandbox) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole sandbox state with a snapshot, all or nothing.
func (s *WasmPluginSandbox) Restore(data []byte) error {
	return errNotImplemented
}
