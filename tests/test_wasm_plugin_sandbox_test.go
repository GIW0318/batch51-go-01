package task

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-06"
)

// stubRunner is the injected executor used by the tests. It reports the fuel
// it consumed so the sandbox can enforce the per call budget.
type stubRunner struct {
	mu     sync.Mutex
	calls  int
	output []byte
	fuel   uint64
	fail   error
}

func (r *stubRunner) Run(fn string, args []byte, fuel uint64) (impl.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.fail != nil {
		return impl.RunResult{}, r.fail
	}
	out := r.output
	if out == nil {
		out = []byte("ok:" + fn)
	}
	used := r.fuel
	if used == 0 {
		used = 10
	}
	return impl.RunResult{Output: out, FuelUsed: used}, nil
}

func (r *stubRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func newSandbox(t *testing.T, runner impl.Runner, tweak func(*impl.SandboxConfig)) (*impl.WasmPluginSandbox, string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if err := os.MkdirAll(filepath.Join(root, "plugins", name), 0o755); err != nil {
			t.Fatalf("prepare plugin dir %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "private"), 0o755); err != nil {
		t.Fatalf("prepare private dir: %v", err)
	}
	cfg := impl.SandboxConfig{
		RootDir:               root,
		MaxPlugins:            4,
		MaxInstancesPerPlugin: 2,
		FuelPerCall:           1000,
		MaxMemoryBytes:        512,
		MaxSnapshotBytes:      1 << 20,
		Runner:                runner,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	return impl.NewWasmPluginSandbox(cfg), root
}

func manifestFor(root, name string) impl.Manifest {
	return impl.Manifest{
		Name:          name,
		Entry:         "_start",
		Preopens:      []impl.Preopen{{Path: filepath.Join(root, "plugins", name), ReadOnly: true}},
		HostFunctions: []string{"kv_read", "kv_write"},
		Env:           []string{"LOG_LEVEL=warn"},
	}
}

func TestLoadRegistersPluginInLoadedState(t *testing.T) {
	runner := &stubRunner{}
	s, root := newSandbox(t, runner, nil)
	id, err := s.Load("alpha", []byte("wasm:alpha:v1"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("load must succeed, got %v", err)
	}
	if id == "" {
		t.Fatalf("load must return a non empty plugin id")
	}
	infos := s.Plugins()
	if len(infos) != 1 {
		t.Fatalf("plugin table size = %d, want 1", len(infos))
	}
	if infos[0].ID != id || infos[0].Name != "alpha" {
		t.Fatalf("plugin entry = %+v, want id %q name alpha", infos[0], id)
	}
	if infos[0].State != impl.PluginLoaded {
		t.Fatalf("plugin state = %q, want %q", infos[0].State, impl.PluginLoaded)
	}
	if infos[0].Instances != 0 {
		t.Fatalf("fresh plugin instance count = %d, want 0", infos[0].Instances)
	}
	usage, err := s.Stats(id)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if usage.State != impl.PluginLoaded || usage.Calls != 0 || usage.FuelUsed != 0 {
		t.Fatalf("fresh usage = %+v, want loaded with zero counters", usage)
	}
}

func TestLoadRejectsBlankNameAndEmptyCode(t *testing.T) {
	s, root := newSandbox(t, &stubRunner{}, nil)
	if _, err := s.Load("   ", []byte("wasm"), manifestFor(root, "alpha")); !errors.Is(err, impl.ErrInvalidPlugin) {
		t.Fatalf("blank plugin name must fail with ErrInvalidPlugin, got %v", err)
	}
	if _, err := s.Load("alpha", nil, manifestFor(root, "alpha")); !errors.Is(err, impl.ErrInvalidPlugin) {
		t.Fatalf("empty plugin code must fail with ErrInvalidPlugin, got %v", err)
	}
	if got := len(s.Plugins()); got != 0 {
		t.Fatalf("rejected loads created %d plugins, want 0", got)
	}
}

func TestLoadRejectsDuplicatePluginName(t *testing.T) {
	s, root := newSandbox(t, &stubRunner{}, nil)
	id, err := s.Load("alpha", []byte("wasm:alpha:v1"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if _, err := s.Load("alpha", []byte("wasm:alpha:v2"), manifestFor(root, "alpha")); !errors.Is(err, impl.ErrDuplicatePlugin) {
		t.Fatalf("duplicate plugin name must fail with ErrDuplicatePlugin, got %v", err)
	}
	if got := len(s.Plugins()); got != 1 {
		t.Fatalf("plugin table size after duplicate load = %d, want 1", got)
	}
	usage, err := s.Stats(id)
	if err != nil {
		t.Fatalf("stats after duplicate load: %v", err)
	}
	if usage.State != impl.PluginLoaded {
		t.Fatalf("original plugin state = %q, want %q", usage.State, impl.PluginLoaded)
	}
}

func TestLoadEnforcesPluginLimit(t *testing.T) {
	s, root := newSandbox(t, &stubRunner{}, func(cfg *impl.SandboxConfig) {
		cfg.MaxPlugins = 2
	})
	for _, name := range []string{"alpha", "beta"} {
		if _, err := s.Load(name, []byte("wasm:"+name), manifestFor(root, name)); err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
	}
	if _, err := s.Load("gamma", []byte("wasm:gamma"), manifestFor(root, "gamma")); !errors.Is(err, impl.ErrPluginLimit) {
		t.Fatalf("third plugin must hit the cap with ErrPluginLimit, got %v", err)
	}
	if got := len(s.Plugins()); got != 2 {
		t.Fatalf("plugin table size = %d, want the cap 2", got)
	}
}

func TestInstantiateEnforcesInstanceLimit(t *testing.T) {
	s, root := newSandbox(t, &stubRunner{}, func(cfg *impl.SandboxConfig) {
		cfg.MaxInstancesPerPlugin = 2
	})
	id, err := s.Load("alpha", []byte("wasm:alpha"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	started, limited := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Instantiate(id)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				started++
			case errors.Is(err, impl.ErrInstanceLimit):
				limited++
			}
		}()
	}
	wg.Wait()
	if started != 2 {
		t.Fatalf("concurrent instantiations started %d instances, want exactly the cap 2", started)
	}
	if limited != 10 {
		t.Fatalf("concurrent instantiations reported %d limit errors, want 10", limited)
	}
	if got := s.Instances(id); got != 2 {
		t.Fatalf("live instance count = %d, want 2", got)
	}
}

func TestInstantiateRejectsUnknownPlugin(t *testing.T) {
	s, _ := newSandbox(t, &stubRunner{}, nil)
	if _, err := s.Instantiate("plugin-404"); !errors.Is(err, impl.ErrUnknownPlugin) {
		t.Fatalf("unknown plugin must fail with ErrUnknownPlugin, got %v", err)
	}
	if _, err := s.Instantiate(""); !errors.Is(err, impl.ErrUnknownPlugin) {
		t.Fatalf("empty plugin id must fail with ErrUnknownPlugin, got %v", err)
	}
	if got := s.Instances("plugin-404"); got != 0 {
		t.Fatalf("unknown plugin reports %d instances, want 0", got)
	}
}

func TestCallRunsDeclaredHostFunction(t *testing.T) {
	runner := &stubRunner{output: []byte("value-42"), fuel: 7}
	s, root := newSandbox(t, runner, nil)
	id, err := s.Load("alpha", []byte("wasm:alpha"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	inst, err := s.Instantiate(id)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if inst.ID == "" || inst.PluginID != id {
		t.Fatalf("instance = %+v, want a generated id bound to %q", inst, id)
	}
	out, err := s.Call(inst, "kv_read", []byte("key"))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if string(out) != "value-42" {
		t.Fatalf("call output = %q, want the executor output %q", out, "value-42")
	}
	if runner.callCount() != 1 {
		t.Fatalf("executor invocations = %d, want 1", runner.callCount())
	}
	usage, err := s.Stats(id)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if usage.Calls != 1 || usage.FuelUsed != 7 {
		t.Fatalf("usage calls=%d fuel=%d, want 1 and 7", usage.Calls, usage.FuelUsed)
	}
	if usage.Instances != 1 || usage.State != impl.PluginActive {
		t.Fatalf("usage = %+v, want one instance in %q state", usage, impl.PluginActive)
	}
}

func TestCallRejectsUndeclaredHostFunction(t *testing.T) {
	runner := &stubRunner{}
	s, root := newSandbox(t, runner, nil)
	id, err := s.Load("alpha", []byte("wasm:alpha"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	inst, err := s.Instantiate(id)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if _, err := s.Call(inst, "net_dial", []byte("host:443")); !errors.Is(err, impl.ErrHostFunctionDenied) {
		t.Fatalf("undeclared host function must fail with ErrHostFunctionDenied, got %v", err)
	}
	if runner.callCount() != 0 {
		t.Fatalf("executor ran %d times for a denied import, want 0", runner.callCount())
	}
	usage, err := s.Stats(id)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if usage.Calls != 0 || usage.FuelUsed != 0 {
		t.Fatalf("denied call changed accounting: %+v", usage)
	}
}

func TestCallRejectsArgumentsOverMemoryLimit(t *testing.T) {
	runner := &stubRunner{}
	s, root := newSandbox(t, runner, func(cfg *impl.SandboxConfig) {
		cfg.MaxMemoryBytes = 32
	})
	id, err := s.Load("alpha", []byte("wasm:alpha"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	inst, err := s.Instantiate(id)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	big := make([]byte, 64)
	if _, err := s.Call(inst, "kv_read", big); !errors.Is(err, impl.ErrMemoryLimit) {
		t.Fatalf("oversized arguments must fail with ErrMemoryLimit, got %v", err)
	}
	if runner.callCount() != 0 {
		t.Fatalf("executor ran %d times for an oversized call, want 0", runner.callCount())
	}
	if _, err := s.Call(inst, "kv_read", make([]byte, 32)); err != nil {
		t.Fatalf("arguments exactly at the memory limit must pass, got %v", err)
	}
}

func TestCheckPathHonoursDeclaredPreopens(t *testing.T) {
	s, root := newSandbox(t, &stubRunner{}, nil)
	id, err := s.Load("alpha", []byte("wasm:alpha"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	inst, err := s.Instantiate(id)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	visible := filepath.Join(root, "plugins", "alpha", "data.bin")
	if err := os.WriteFile(visible, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write visible file: %v", err)
	}
	if err := s.CheckPath(inst, visible); err != nil {
		t.Fatalf("path inside a declared preopen must be visible, got %v", err)
	}
	hidden := filepath.Join(root, "private", "secret.bin")
	if err := s.CheckPath(inst, hidden); !errors.Is(err, impl.ErrPathDenied) {
		t.Fatalf("path outside every preopen must fail with ErrPathDenied, got %v", err)
	}
	if err := s.CheckPath(inst, "/etc/passwd"); !errors.Is(err, impl.ErrPathEscape) {
		t.Fatalf("path outside the sandbox root must fail with ErrPathEscape, got %v", err)
	}
}

func TestRevokeBlocksNewCalls(t *testing.T) {
	s, root := newSandbox(t, &stubRunner{}, nil)
	id, err := s.Load("alpha", []byte("wasm:alpha"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	inst, err := s.Instantiate(id)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if _, err := s.Call(inst, "kv_read", []byte("k")); err != nil {
		t.Fatalf("call before revoke: %v", err)
	}
	if err := s.Revoke(id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.Call(inst, "kv_read", []byte("k")); !errors.Is(err, impl.ErrPluginRevoked) {
		t.Fatalf("call after revoke must fail with ErrPluginRevoked, got %v", err)
	}
	usage, err := s.Stats(id)
	if err != nil {
		t.Fatalf("stats after revoke: %v", err)
	}
	if usage.State != impl.PluginRevoked {
		t.Fatalf("revoked plugin state = %q, want %q", usage.State, impl.PluginRevoked)
	}
	if usage.Instances != 0 || s.Instances(id) != 0 {
		t.Fatalf("revoke left %d instances alive, want 0", usage.Instances)
	}
	if err := s.Revoke(id); !errors.Is(err, impl.ErrPluginRevoked) {
		t.Fatalf("revoking twice must fail with ErrPluginRevoked, got %v", err)
	}
}

func TestCallFuelOverrunMarksPluginFailed(t *testing.T) {
	runner := &stubRunner{fuel: 51}
	s, root := newSandbox(t, runner, func(cfg *impl.SandboxConfig) {
		cfg.FuelPerCall = 50
	})
	id, err := s.Load("alpha", []byte("wasm:alpha"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	inst, err := s.Instantiate(id)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if _, err := s.Call(inst, "kv_read", []byte("k")); !errors.Is(err, impl.ErrFuelExhausted) {
		t.Fatalf("overrun must fail with ErrFuelExhausted, got %v", err)
	}
	usage, err := s.Stats(id)
	if err != nil {
		t.Fatalf("stats after fuel overrun: %v", err)
	}
	if usage.State != impl.PluginFailed {
		t.Fatalf("plugin state after fuel overrun = %q, want %q", usage.State, impl.PluginFailed)
	}
}

func TestStatsAccumulateCallsAndFuel(t *testing.T) {
	runner := &stubRunner{fuel: 7}
	s, root := newSandbox(t, runner, nil)
	id, err := s.Load("alpha", []byte("wasm:alpha"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	inst, err := s.Instantiate(id)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.Call(inst, "kv_read", []byte("k")); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	usage, err := s.Stats(id)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if usage.Calls != 2 {
		t.Fatalf("call counter = %d, want 2", usage.Calls)
	}
	if usage.FuelUsed != 14 {
		t.Fatalf("fuel counter = %d, want 14", usage.FuelUsed)
	}
	infos := s.Plugins()
	if len(infos) != 1 || infos[0].Instances != 1 {
		t.Fatalf("plugin list = %+v, want one plugin with one instance", infos)
	}
}

func TestSnapshotRestoreKeepsTablesAndCounters(t *testing.T) {
	s, root := newSandbox(t, &stubRunner{fuel: 10}, nil)
	alphaID, err := s.Load("alpha", []byte("wasm:alpha"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("load alpha: %v", err)
	}
	inst, err := s.Instantiate(alphaID)
	if err != nil {
		t.Fatalf("instantiate alpha: %v", err)
	}
	if _, err := s.Call(inst, "kv_read", []byte("k")); err != nil {
		t.Fatalf("call alpha: %v", err)
	}
	betaID, err := s.Load("beta", []byte("wasm:beta"), manifestFor(root, "beta"))
	if err != nil {
		t.Fatalf("load beta: %v", err)
	}
	if err := s.Revoke(betaID); err != nil {
		t.Fatalf("revoke beta: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored, _ := newSandbox(t, &stubRunner{fuel: 10}, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	infos := restored.Plugins()
	if len(infos) != 2 {
		t.Fatalf("restored plugin table size = %d, want 2", len(infos))
	}
	if infos[0].Name != "alpha" || infos[0].State != impl.PluginActive || infos[0].Instances != 1 {
		t.Fatalf("restored alpha = %+v, want active with one instance", infos[0])
	}
	if infos[1].Name != "beta" || infos[1].State != impl.PluginRevoked {
		t.Fatalf("restored beta = %+v, want revoked", infos[1])
	}
	usage, err := restored.Stats(alphaID)
	if err != nil {
		t.Fatalf("restored stats: %v", err)
	}
	if usage.Calls != 1 || usage.FuelUsed != 10 {
		t.Fatalf("restored counters calls=%d fuel=%d, want 1 and 10", usage.Calls, usage.FuelUsed)
	}
	if _, err := restored.Call(inst, "kv_read", []byte("k")); err != nil {
		t.Fatalf("restored instance must keep working, got %v", err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	s, root := newSandbox(t, &stubRunner{fuel: 10}, nil)
	id, err := s.Load("alpha", []byte("wasm:alpha"), manifestFor(root, "alpha"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	inst, err := s.Instantiate(id)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if _, err := s.Call(inst, "kv_read", []byte("k")); err != nil {
		t.Fatalf("call: %v", err)
	}
	if err := s.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	usage, err := s.Stats(id)
	if err != nil {
		t.Fatalf("live stats after rejected restore: %v", err)
	}
	if usage.Calls != 1 || usage.State != impl.PluginActive {
		t.Fatalf("rejected restore touched live state: %+v", usage)
	}
	if _, err := s.Call(inst, "kv_read", []byte("k")); err != nil {
		t.Fatalf("live instance must keep working after a rejected restore, got %v", err)
	}
}
