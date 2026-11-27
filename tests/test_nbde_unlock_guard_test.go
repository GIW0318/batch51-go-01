package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-c-03"
)

const (
	tangServerA = "tang-a.example"
	tangServerB = "tang-b.example"
	pinOne      = impl.Thumbprint("tp-adv-1")
	pinTwo      = impl.Thumbprint("tp-adv-2")
)

func unlockConfig() impl.UnlockConfig {
	return impl.UnlockConfig{
		Servers:      []string{tangServerA, tangServerB},
		MaxKeys:      4,
		MaxServers:   4,
		MaxBindings:  4,
		RotateWindow: time.Hour,
	}
}

func advKeySet() []impl.JWK {
	return []impl.JWK{
		{Thumbprint: "tp-key-1", Key: "key-material-1"},
		{Thumbprint: "tp-key-2", Key: "key-material-2"},
	}
}

func testAdv(server string) impl.Advertisement {
	return impl.Advertisement{Server: server, Keys: advKeySet(), Thumbprint: string(pinOne)}
}

func bindVolume(t *testing.T, g *impl.NbdeUnlockGuard, volume, server string) impl.Binding {
	t.Helper()
	adv := testAdv(server)
	if err := g.PublishAdv(server, adv); err != nil {
		t.Fatalf("publish advertisement for %s: %v", server, err)
	}
	b, err := g.Bind(volume, adv, pinOne)
	if err != nil {
		t.Fatalf("bind volume %s: %v", volume, err)
	}
	return b
}

func TestBindStoresPinnedKeys(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	b := bindVolume(t, g, "vol-1", tangServerA)
	if b.Volume != "vol-1" {
		t.Fatalf("binding volume = %q, want vol-1", b.Volume)
	}
	if b.Pin != pinOne {
		t.Fatalf("binding pin = %q, want %q", b.Pin, pinOne)
	}
	if b.State != impl.BindingActive {
		t.Fatalf("binding state = %q, want %q", b.State, impl.BindingActive)
	}
	if len(b.Keys) != 2 || b.Keys[0] != "tp-key-1" || b.Keys[1] != "tp-key-2" {
		t.Fatalf("binding keys = %v, want the two advertised thumbprints", b.Keys)
	}
	got, ok := g.Binding("vol-1")
	if !ok || got.Pin != pinOne {
		t.Fatalf("Binding() = %+v ok=%v, want the pinned record", got, ok)
	}
	if _, ok := g.Binding("vol-404"); ok {
		t.Fatalf("an unbound volume must not report a binding")
	}
}

func TestBindRejectsBlankVolume(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	if _, err := g.Bind("   ", testAdv(tangServerA), pinOne); !errors.Is(err, impl.ErrBadVolume) {
		t.Fatalf("blank volume = %v, want ErrBadVolume", err)
	}
	if _, ok := g.Binding("   "); ok {
		t.Fatalf("a rejected bind must not create a record")
	}
}

func TestBindRejectsBlankThumbprint(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	if _, err := g.Bind("vol-2", testAdv(tangServerA), impl.Thumbprint("  ")); !errors.Is(err, impl.ErrBadThumbprint) {
		t.Fatalf("blank pin = %v, want ErrBadThumbprint", err)
	}
}

func TestBindRejectsThumbprintMismatch(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	if _, err := g.Bind("vol-3", testAdv(tangServerA), pinTwo); !errors.Is(err, impl.ErrThumbprintMismatch) {
		t.Fatalf("pin differing from the advertisement = %v, want ErrThumbprintMismatch", err)
	}
	if _, ok := g.Binding("vol-3"); ok {
		t.Fatalf("a mismatched pin must not create a binding")
	}
}

func TestBindRejectsDuplicateVolume(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	bindVolume(t, g, "vol-4", tangServerA)
	if _, err := g.Bind("vol-4", testAdv(tangServerA), pinOne); !errors.Is(err, impl.ErrAlreadyBound) {
		t.Fatalf("second bind of the same volume = %v, want ErrAlreadyBound", err)
	}
}

func TestBindRejectsTooManyKeys(t *testing.T) {
	cfg := unlockConfig()
	cfg.MaxKeys = 2
	g := impl.NewNbdeUnlockGuard(cfg)
	adv := impl.Advertisement{
		Server: tangServerA,
		Keys: []impl.JWK{
			{Thumbprint: "tp-key-1", Key: "k1"},
			{Thumbprint: "tp-key-2", Key: "k2"},
			{Thumbprint: "tp-key-3", Key: "k3"},
		},
		Thumbprint: string(pinOne),
	}
	if _, err := g.Bind("vol-5", adv, pinOne); !errors.Is(err, impl.ErrAdvKeyLimit) {
		t.Fatalf("advertisement with three keys = %v, want ErrAdvKeyLimit", err)
	}
}

func TestVerifyAdvRejectsEmptyKeys(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	adv := impl.Advertisement{Server: tangServerA, Thumbprint: string(pinOne)}
	if err := g.VerifyAdv(adv); !errors.Is(err, impl.ErrBadAdvertisement) {
		t.Fatalf("advertisement without keys = %v, want ErrBadAdvertisement", err)
	}
}

func TestVerifyAdvRejectsBlankKeyThumbprint(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	adv := impl.Advertisement{
		Server:     tangServerA,
		Keys:       []impl.JWK{{Thumbprint: "", Key: "k1"}},
		Thumbprint: string(pinOne),
	}
	if err := g.VerifyAdv(adv); !errors.Is(err, impl.ErrBadAdvertisement) {
		t.Fatalf("advertisement with a blank key thumbprint = %v, want ErrBadAdvertisement", err)
	}
}

func TestUnlockRejectsUnconfiguredServer(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	bindVolume(t, g, "vol-6", tangServerA)
	if _, err := g.Unlock("vol-6", "tang-evil.example"); !errors.Is(err, impl.ErrServerNotAllowed) {
		t.Fatalf("unlock via an unlisted server = %v, want ErrServerNotAllowed", err)
	}
	if _, err := g.Unlock("vol-404", tangServerA); !errors.Is(err, impl.ErrNotBound) {
		t.Fatalf("unlock of an unbound volume = %v, want ErrNotBound", err)
	}
}

func TestUnlockRequiresEscrow(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	bindVolume(t, g, "vol-7", tangServerA)
	if _, err := g.Unlock("vol-7", tangServerA); !errors.Is(err, impl.ErrNoEscrow) {
		t.Fatalf("unlock without a local escrow key = %v, want ErrNoEscrow", err)
	}
}

func TestUnlockReturnsEscrowedKey(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	bindVolume(t, g, "vol-8", tangServerA)
	if err := g.SetEscrow("vol-8", "escrow-abc"); err != nil {
		t.Fatalf("set escrow: %v", err)
	}
	vk, err := g.Unlock("vol-8", tangServerA)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if vk.Key != "escrow-abc" {
		t.Fatalf("volume key = %q, want the escrow key", vk.Key)
	}
	if vk.Volume != "vol-8" || vk.Server != tangServerA {
		t.Fatalf("volume key = %+v, want volume vol-8 from %s", vk, tangServerA)
	}
}

func TestUnlockRejectsMissingPinnedKey(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	bindVolume(t, g, "vol-9", tangServerA)
	if err := g.SetEscrow("vol-9", "escrow-def"); err != nil {
		t.Fatalf("set escrow: %v", err)
	}
	shrunken := impl.Advertisement{
		Server:     tangServerB,
		Keys:       []impl.JWK{{Thumbprint: "tp-key-1", Key: "key-material-1"}},
		Thumbprint: string(pinOne),
	}
	if err := g.PublishAdv(tangServerB, shrunken); err != nil {
		t.Fatalf("publish shrunken advertisement: %v", err)
	}
	if _, err := g.Unlock("vol-9", tangServerB); !errors.Is(err, impl.ErrPinnedKeyMissing) {
		t.Fatalf("advertisement without every pinned key = %v, want ErrPinnedKeyMissing", err)
	}
}

func TestUnlockFallsBackWhenServerUnreachable(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	bindVolume(t, g, "vol-10", tangServerA)
	if err := g.SetEscrow("vol-10", "escrow-ghi"); err != nil {
		t.Fatalf("set escrow: %v", err)
	}
	if _, err := g.Unlock("vol-10", tangServerB); !errors.Is(err, impl.ErrServerUnreachable) {
		t.Fatalf("unlock against a silent server = %v, want ErrServerUnreachable", err)
	}
	pw, err := g.FallbackPassword("vol-10")
	if err != nil {
		t.Fatalf("fallback password: %v", err)
	}
	if pw != "recovery:escrow-ghi" {
		t.Fatalf("fallback password = %q, want recovery:escrow-ghi", pw)
	}
	if _, err := g.FallbackPassword("vol-404"); !errors.Is(err, impl.ErrNotBound) {
		t.Fatalf("fallback for an unbound volume = %v, want ErrNotBound", err)
	}
}

func TestRotateRequiresActorAndNewPin(t *testing.T) {
	g := impl.NewNbdeUnlockGuard(unlockConfig())
	bindVolume(t, g, "vol-11", tangServerA)
	next := impl.Advertisement{Server: tangServerA, Keys: advKeySet(), Thumbprint: string(pinTwo)}
	if err := g.Rotate("vol-11", impl.RotateRequest{Adv: next, Pin: pinTwo, Actor: "  "}); !errors.Is(err, impl.ErrBadActor) {
		t.Fatalf("rotation without an actor = %v, want ErrBadActor", err)
	}
	if err := g.Rotate("vol-11", impl.RotateRequest{Adv: next, Pin: pinOne, Actor: "ops-1"}); !errors.Is(err, impl.ErrSameThumbprint) {
		t.Fatalf("rotation reusing the pin = %v, want ErrSameThumbprint", err)
	}
	if err := g.Rotate("vol-11", impl.RotateRequest{Adv: next, Pin: pinTwo, Actor: "ops-1"}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	b, _ := g.Binding("vol-11")
	if b.State != impl.BindingRotating {
		t.Fatalf("binding state during the window = %q, want %q", b.State, impl.BindingRotating)
	}
	if err := g.FinishRotate("vol-11"); err != nil {
		t.Fatalf("finish rotate: %v", err)
	}
	b, _ = g.Binding("vol-11")
	if b.Pin != pinTwo || b.State != impl.BindingActive {
		t.Fatalf("binding after rotation = %+v, want pin %q and active", b, pinTwo)
	}
	if err := g.FinishRotate("vol-11"); !errors.Is(err, impl.ErrNotRotating) {
		t.Fatalf("finishing twice = %v, want ErrNotRotating", err)
	}
	if err := g.Rotate("vol-404", impl.RotateRequest{Adv: next, Pin: pinTwo, Actor: "ops-1"}); !errors.Is(err, impl.ErrNotBound) {
		t.Fatalf("rotation of an unbound volume = %v, want ErrNotBound", err)
	}
}

func TestSnapshotRestoreKeepsBindings(t *testing.T) {
	cfg := unlockConfig()
	g := impl.NewNbdeUnlockGuard(cfg)
	bindVolume(t, g, "vol-12", tangServerA)
	if err := g.SetEscrow("vol-12", "escrow-jkl"); err != nil {
		t.Fatalf("set escrow: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewNbdeUnlockGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	b, ok := restored.Binding("vol-12")
	if !ok || b.Pin != pinOne || len(b.Keys) != 2 {
		t.Fatalf("restored binding = %+v ok=%v, want the pinned record", b, ok)
	}
	vk, err := restored.Unlock("vol-12", tangServerA)
	if err != nil {
		t.Fatalf("unlock after restore: %v", err)
	}
	if vk.Key != "escrow-jkl" {
		t.Fatalf("restored volume key = %q, want the escrow key", vk.Key)
	}
	if err := restored.Restore([]byte("{not-a-binding-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot = %v, want ErrBadSnapshot", err)
	}
	if _, ok := restored.Binding("vol-12"); !ok {
		t.Fatalf("a rejected restore must keep the live binding")
	}
}
