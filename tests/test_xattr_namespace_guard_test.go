package task

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-d-08"
)

func xattrConfig() impl.GuardConfig {
	return impl.GuardConfig{MaxValueBytes: 64, MaxAttrsPerFile: 64}
}

func ownerCred() impl.Cred {
	return impl.Cred{UID: 1000}
}

func privilegedCred() impl.Cred {
	return impl.Cred{UID: 0, Privileged: true}
}

func trustedCred() impl.Cred {
	return impl.Cred{UID: 0, Trusted: true}
}

func newMountedGuard(t *testing.T, cfg impl.GuardConfig) *impl.XattrNamespaceGuard {
	t.Helper()
	g := impl.NewXattrNamespaceGuard(cfg)
	opts := impl.FSOptions{Type: "ext4", SupportsTrusted: true, SupportsSecurity: true, SupportsACL: true}
	if err := g.Mount("/data", opts); err != nil {
		t.Fatalf("mount /data: %v", err)
	}
	return g
}

func createFile(t *testing.T, g *impl.XattrNamespaceGuard, path string, mode uint32, owner int) {
	t.Helper()
	if err := g.Create(path, mode, owner); err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
}

func TestSetUserAttrRequiresOwner(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/app.conf", 0640, 1000)
	state, err := g.Set("/data/app.conf", impl.Attribute{Name: "user.tag", Value: "abc"}, ownerCred())
	if err != nil {
		t.Fatalf("owner must be allowed to set a user attribute, got %v", err)
	}
	if state.Namespace != impl.NSUser {
		t.Fatalf("namespace = %q, want %q", state.Namespace, impl.NSUser)
	}
	if state.Version != 1 || state.FileVersion != 1 {
		t.Fatalf("revisions after the first write: attr=%d file=%d, want 1 and 1", state.Version, state.FileVersion)
	}
	if state.Bytes != 3 {
		t.Fatalf("value bytes = %d, want 3", state.Bytes)
	}
	if _, err := g.Set("/data/app.conf", impl.Attribute{Name: "user.tag", Value: "x"}, impl.Cred{UID: 2000}); !errors.Is(err, impl.ErrPermission) {
		t.Fatalf("a foreign user must fail with ErrPermission, got %v", err)
	}
	got, err := g.Get("/data/app.conf", "user.tag", ownerCred())
	if err != nil {
		t.Fatalf("get user attribute: %v", err)
	}
	if got.Value != "abc" || got.Version != 1 {
		t.Fatalf("stored attribute mismatch: %+v", got)
	}
}

func TestSetRejectsBadNamespace(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/ns.conf", 0644, 1000)
	for _, name := range []string{"vendor.tag", "", "user", "Trusted.tag"} {
		if _, err := g.Set("/data/ns.conf", impl.Attribute{Name: name, Value: "v"}, privilegedCred()); !errors.Is(err, impl.ErrBadNamespace) {
			t.Fatalf("attribute name %q must fail with ErrBadNamespace, got %v", name, err)
		}
	}
	if got := len(g.Attrs("/data/ns.conf")); got != 0 {
		t.Fatalf("rejected writes left %d attributes behind", got)
	}
}

func TestSetTrustedRequiresPrivilege(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/trusted.conf", 0600, 1000)
	if _, err := g.Set("/data/trusted.conf", impl.Attribute{Name: "trusted.overlay", Value: "on"}, ownerCred()); !errors.Is(err, impl.ErrPermission) {
		t.Fatalf("trusted namespace without privilege must fail with ErrPermission, got %v", err)
	}
	state, err := g.Set("/data/trusted.conf", impl.Attribute{Name: "trusted.overlay", Value: "on"}, privilegedCred())
	if err != nil {
		t.Fatalf("privileged write into trusted must succeed, got %v", err)
	}
	if state.Namespace != impl.NSTrusted {
		t.Fatalf("namespace = %q, want %q", state.Namespace, impl.NSTrusted)
	}
}

func TestSetSecurityRequiresTrustedComponent(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/label.conf", 0600, 1000)
	if _, err := g.Set("/data/label.conf", impl.Attribute{Name: "security.selinux", Value: "s0"}, privilegedCred()); !errors.Is(err, impl.ErrPermission) {
		t.Fatalf("security namespace from an untrusted component must fail with ErrPermission, got %v", err)
	}
	state, err := g.Set("/data/label.conf", impl.Attribute{Name: "security.selinux", Value: "s0"}, trustedCred())
	if err != nil {
		t.Fatalf("trusted component must be allowed to set security attributes, got %v", err)
	}
	if state.Namespace != impl.NSSecurity {
		t.Fatalf("namespace = %q, want %q", state.Namespace, impl.NSSecurity)
	}
}

func TestSetEnforcesLimits(t *testing.T) {
	cfg := impl.GuardConfig{MaxValueBytes: 8, MaxAttrsPerFile: 2}
	g := newMountedGuard(t, cfg)
	createFile(t, g, "/data/limit.conf", 0600, 1000)
	if _, err := g.Set("/data/limit.conf", impl.Attribute{Name: "user.big", Value: "0123456789"}, ownerCred()); !errors.Is(err, impl.ErrValueTooLarge) {
		t.Fatalf("oversized value must fail with ErrValueTooLarge, got %v", err)
	}
	if _, err := g.Set("/data/limit.conf", impl.Attribute{Name: "user.one", Value: "1"}, ownerCred()); err != nil {
		t.Fatalf("first attribute must be accepted, got %v", err)
	}
	if _, err := g.Set("/data/limit.conf", impl.Attribute{Name: "user.two", Value: "2"}, ownerCred()); err != nil {
		t.Fatalf("second attribute must be accepted, got %v", err)
	}
	if _, err := g.Set("/data/limit.conf", impl.Attribute{Name: "user.three", Value: "3"}, ownerCred()); !errors.Is(err, impl.ErrAttrLimit) {
		t.Fatalf("third attribute must fail with ErrAttrLimit, got %v", err)
	}
	if got := len(g.Attrs("/data/limit.conf")); got != 2 {
		t.Fatalf("stored attributes = %d, want the configured cap 2", got)
	}
}

func TestSetACLUpdatesEffectiveMode(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/acl.conf", 0644, 1000)
	acl := "u::rwx,g::r-x,o::---,m::r-x"
	if _, err := g.Set("/data/acl.conf", impl.Attribute{Name: impl.AttrACLAccess, Value: acl}, ownerCred()); err != nil {
		t.Fatalf("owner must be allowed to set the access ACL, got %v", err)
	}
	stats, err := g.FileStats("/data/acl.conf")
	if err != nil {
		t.Fatalf("file stats: %v", err)
	}
	if stats.Mode != 0o750 {
		t.Fatalf("effective mode = %#o, want 0750 from the ACL entries", stats.Mode)
	}
	if stats.Version != 1 {
		t.Fatalf("file revision after the ACL write = %d, want 1", stats.Version)
	}
}

func TestSetRejectsMalformedACL(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/badacl.conf", 0644, 1000)
	for _, acl := range []string{"u::rwq,g::r-x,o::---,m::r-x", "u::rwx,o::---,m::r-x", "u::rwx,g::r-x,o::---"} {
		if _, err := g.Set("/data/badacl.conf", impl.Attribute{Name: impl.AttrACLAccess, Value: acl}, ownerCred()); !errors.Is(err, impl.ErrBadACLValue) {
			t.Fatalf("acl %q must fail with ErrBadACLValue, got %v", acl, err)
		}
	}
	if got := len(g.Attrs("/data/badacl.conf")); got != 0 {
		t.Fatalf("rejected ACL writes left %d attributes", got)
	}
}

func TestChmodSyncsACLMask(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/mode.conf", 0644, 1000)
	acl := "u::rwx,g::r-x,o::---,m::r-x"
	if _, err := g.Set("/data/mode.conf", impl.Attribute{Name: impl.AttrACLAccess, Value: acl}, ownerCred()); err != nil {
		t.Fatalf("set acl: %v", err)
	}
	if err := g.Chmod("/data/mode.conf", 0o660, ownerCred()); err != nil {
		t.Fatalf("chmod must succeed, got %v", err)
	}
	stats, err := g.FileStats("/data/mode.conf")
	if err != nil {
		t.Fatalf("file stats: %v", err)
	}
	if stats.Mode != 0o660 {
		t.Fatalf("mode after chmod = %#o, want 0660", stats.Mode)
	}
	attr, err := g.Get("/data/mode.conf", impl.AttrACLAccess, ownerCred())
	if err != nil {
		t.Fatalf("get acl after chmod: %v", err)
	}
	if !strings.Contains(attr.Value, "m::rw-") {
		t.Fatalf("acl mask was not kept in step, got %q", attr.Value)
	}
	if err := g.Chmod("/data/mode.conf", 0o7777, impl.Cred{UID: 2000}); !errors.Is(err, impl.ErrPermission) {
		t.Fatalf("chmod by a foreign user must fail with ErrPermission, got %v", err)
	}
	if err := g.Chmod("/data/mode.conf", 0o10000, ownerCred()); !errors.Is(err, impl.ErrBadMode) {
		t.Fatalf("mode above 07777 must fail with ErrBadMode, got %v", err)
	}
}

func TestGetRemoveErrors(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/err.conf", 0640, 1000)
	if _, err := g.Get("/data/missing.conf", "user.a", ownerCred()); !errors.Is(err, impl.ErrUnknownPath) {
		t.Fatalf("get on an unknown path must fail with ErrUnknownPath, got %v", err)
	}
	if _, err := g.Get("/data/err.conf", "user.absent", ownerCred()); !errors.Is(err, impl.ErrAttrNotFound) {
		t.Fatalf("get of a missing attribute must fail with ErrAttrNotFound, got %v", err)
	}
	if err := g.Remove("/data/err.conf", "user.absent", ownerCred()); !errors.Is(err, impl.ErrAttrNotFound) {
		t.Fatalf("remove of a missing attribute must fail with ErrAttrNotFound, got %v", err)
	}
	if _, err := g.Set("/data/err.conf", impl.Attribute{Name: "user.keep", Value: "v"}, ownerCred()); err != nil {
		t.Fatalf("set user attribute: %v", err)
	}
	if err := g.Remove("/data/err.conf", "user.keep", impl.Cred{UID: 2000}); !errors.Is(err, impl.ErrPermission) {
		t.Fatalf("remove by a foreign user must fail with ErrPermission, got %v", err)
	}
	if err := g.Remove("/data/err.conf", "user.keep", ownerCred()); err != nil {
		t.Fatalf("owner remove must succeed, got %v", err)
	}
	if _, err := g.Get("/data/err.conf", "user.keep", ownerCred()); !errors.Is(err, impl.ErrAttrNotFound) {
		t.Fatalf("removed attribute must be gone, got %v", err)
	}
}

func TestListFiltersByPrivilege(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/list.conf", 0640, 1000)
	writes := []struct {
		attr impl.Attribute
		cred impl.Cred
	}{
		{impl.Attribute{Name: "user.a", Value: "1"}, ownerCred()},
		{impl.Attribute{Name: "trusted.b", Value: "2"}, privilegedCred()},
		{impl.Attribute{Name: "security.c", Value: "3"}, trustedCred()},
		{impl.Attribute{Name: impl.AttrACLAccess, Value: "u::rw-,g::r--,o::---,m::r--"}, ownerCred()},
	}
	for _, w := range writes {
		if _, err := g.Set("/data/list.conf", w.attr, w.cred); err != nil {
			t.Fatalf("set %s: %v", w.attr.Name, err)
		}
	}
	plain, err := g.List("/data/list.conf", impl.Cred{UID: 1000})
	if err != nil {
		t.Fatalf("list as the owner: %v", err)
	}
	wantPlain := []string{impl.AttrACLAccess, "user.a"}
	if len(plain) != len(wantPlain) {
		t.Fatalf("plain listing = %v, want %v", plain, wantPlain)
	}
	for i, name := range wantPlain {
		if plain[i] != name {
			t.Fatalf("plain listing = %v, want the sorted %v", plain, wantPlain)
		}
	}
	privileged, err := g.List("/data/list.conf", privilegedCred())
	if err != nil {
		t.Fatalf("list as a privileged caller: %v", err)
	}
	if len(privileged) != 3 || privileged[1] != "trusted.b" {
		t.Fatalf("privileged listing = %v, want user, trusted and system entries", privileged)
	}
	trusted, err := g.List("/data/list.conf", trustedCred())
	if err != nil {
		t.Fatalf("list as a trusted component: %v", err)
	}
	if len(trusted) != 3 || trusted[0] != "security.c" {
		t.Fatalf("trusted listing = %v, want the security entry first", trusted)
	}
}

func TestCopyCopiesAllAttrs(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/source.bin", 0644, 1000)
	createFile(t, g, "/data/target.bin", 0600, 1000)
	if _, err := g.Set("/data/source.bin", impl.Attribute{Name: "user.a", Value: "1"}, ownerCred()); err != nil {
		t.Fatalf("set user.a: %v", err)
	}
	if _, err := g.Set("/data/source.bin", impl.Attribute{Name: "trusted.b", Value: "2"}, privilegedCred()); err != nil {
		t.Fatalf("set trusted.b: %v", err)
	}
	if _, err := g.Set("/data/source.bin", impl.Attribute{Name: impl.AttrACLAccess, Value: "u::rwx,g::r-x,o::---,m::r-x"}, ownerCred()); err != nil {
		t.Fatalf("set acl: %v", err)
	}
	changes, err := g.Copy("/data/source.bin", "/data/target.bin", impl.CopyMode{FollowLinks: true})
	if err != nil {
		t.Fatalf("copy must succeed, got %v", err)
	}
	if len(changes) != 3 {
		t.Fatalf("copy reported %d changes, want 3", len(changes))
	}
	for _, change := range changes {
		if change.Action != impl.ActionCopied {
			t.Fatalf("change %s action = %q, want %q", change.Name, change.Action, impl.ActionCopied)
		}
	}
	if changes[0].Name != impl.AttrACLAccess || changes[1].Name != "trusted.b" || changes[2].Name != "user.a" {
		t.Fatalf("copy report order = %v, want the sorted attribute names", changes)
	}
	if got := len(g.Attrs("/data/target.bin")); got != 3 {
		t.Fatalf("target attribute count = %d, want 3", got)
	}
	attr, err := g.Get("/data/target.bin", "trusted.b", privilegedCred())
	if err != nil {
		t.Fatalf("target must carry the trusted attribute, got %v", err)
	}
	if attr.Value != "2" {
		t.Fatalf("copied value = %q, want 2", attr.Value)
	}
}

func TestCopyDropsUnsupportedACL(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	plainOpts := impl.FSOptions{Type: "vfat", SupportsTrusted: false, SupportsSecurity: false, SupportsACL: false}
	if err := g.Mount("/plain", plainOpts); err != nil {
		t.Fatalf("mount /plain: %v", err)
	}
	createFile(t, g, "/data/origin.bin", 0644, 1000)
	createFile(t, g, "/plain/copy.bin", 0644, 1000)
	if _, err := g.Set("/data/origin.bin", impl.Attribute{Name: "user.a", Value: "1"}, ownerCred()); err != nil {
		t.Fatalf("set user.a: %v", err)
	}
	if _, err := g.Set("/data/origin.bin", impl.Attribute{Name: impl.AttrACLAccess, Value: "u::rwx,g::r-x,o::---,m::r-x"}, ownerCred()); err != nil {
		t.Fatalf("set acl: %v", err)
	}
	changes, err := g.Copy("/data/origin.bin", "/plain/copy.bin", impl.CopyMode{})
	if err != nil {
		t.Fatalf("copy must succeed, got %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("copy reported %d changes, want 2", len(changes))
	}
	if changes[0].Name != impl.AttrACLAccess || changes[0].Action != impl.ActionDropped {
		t.Fatalf("first change = %+v, want the ACL dropped", changes[0])
	}
	if changes[0].Reason != impl.ReasonUnsupported {
		t.Fatalf("drop reason = %q, want %q", changes[0].Reason, impl.ReasonUnsupported)
	}
	if changes[1].Name != "user.a" || changes[1].Action != impl.ActionCopied {
		t.Fatalf("second change = %+v, want the user attribute copied", changes[1])
	}
	if got := len(g.Attrs("/plain/copy.bin")); got != 1 {
		t.Fatalf("target attribute count = %d, want only the user attribute", got)
	}
}

func TestConcurrentSetIncrementsVersionOnce(t *testing.T) {
	cfg := impl.GuardConfig{MaxValueBytes: 64, MaxAttrsPerFile: 64}
	g := newMountedGuard(t, cfg)
	createFile(t, g, "/data/race.conf", 0644, 1000)
	const workers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := make(chan struct{})
	failed := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-started
			_, err := g.Set("/data/race.conf", impl.Attribute{Name: fmt.Sprintf("user.k%02d", i), Value: "v"}, ownerCred())
			if err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}(i)
	}
	close(started)
	wg.Wait()
	if failed != 0 {
		t.Fatalf("%d concurrent writes failed, want 0", failed)
	}
	version, err := g.FileVersion("/data/race.conf")
	if err != nil {
		t.Fatalf("file version: %v", err)
	}
	if version != workers {
		t.Fatalf("file revision = %d, want one bump per write (%d)", version, workers)
	}
	attrs := g.Attrs("/data/race.conf")
	if len(attrs) != workers {
		t.Fatalf("stored attributes = %d, want %d", len(attrs), workers)
	}
	for _, attr := range attrs {
		if attr.Version != 1 {
			t.Fatalf("attribute %s revision = %d, want 1", attr.Name, attr.Version)
		}
	}
}

func TestSnapshotRestoreKeepsAttrs(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/snap.conf", 0640, 1000)
	if _, err := g.Set("/data/snap.conf", impl.Attribute{Name: "user.keep", Value: "kept"}, ownerCred()); err != nil {
		t.Fatalf("set user.keep: %v", err)
	}
	if _, err := g.Set("/data/snap.conf", impl.Attribute{Name: "user.keep", Value: "kept-again"}, ownerCred()); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if _, err := g.Set("/data/snap.conf", impl.Attribute{Name: impl.AttrACLAccess, Value: "u::rwx,g::r-x,o::---,m::r-x"}, ownerCred()); err != nil {
		t.Fatalf("set acl: %v", err)
	}
	version, err := g.FileVersion("/data/snap.conf")
	if err != nil {
		t.Fatalf("file version: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewXattrNamespaceGuard(xattrConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	attr, err := restored.Get("/data/snap.conf", "user.keep", ownerCred())
	if err != nil {
		t.Fatalf("get after restore: %v", err)
	}
	if attr.Value != "kept-again" || attr.Version != 2 {
		t.Fatalf("restored attribute mismatch: %+v", attr)
	}
	restoredVersion, err := restored.FileVersion("/data/snap.conf")
	if err != nil {
		t.Fatalf("file version after restore: %v", err)
	}
	if restoredVersion != version {
		t.Fatalf("restored file revision = %d, want %d", restoredVersion, version)
	}
	restoredNames, err := restored.List("/data/snap.conf", privilegedCred())
	if err != nil {
		t.Fatalf("list after restore: %v", err)
	}
	if len(restoredNames) != 2 {
		t.Fatalf("restored attribute count = %d, want 2", len(restoredNames))
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := newMountedGuard(t, xattrConfig())
	createFile(t, g, "/data/keep.conf", 0640, 1000)
	if _, err := g.Set("/data/keep.conf", impl.Attribute{Name: "user.keep", Value: "v"}, ownerCred()); err != nil {
		t.Fatalf("set user.keep: %v", err)
	}
	if err := g.Restore([]byte("}{broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	attr, err := g.Get("/data/keep.conf", "user.keep", ownerCred())
	if err != nil {
		t.Fatalf("live attribute must survive a rejected restore, got %v", err)
	}
	if attr.Value != "v" || attr.Version != 1 {
		t.Fatalf("failed restore touched the live attribute: %+v", attr)
	}
	version, err := g.FileVersion("/data/keep.conf")
	if err != nil || version != 1 {
		t.Fatalf("live file revision = %d, %v; want 1, nil", version, err)
	}
	if _, err := g.Set("/data/keep.conf", impl.Attribute{Name: "user.after", Value: "v"}, ownerCred()); err != nil {
		t.Fatalf("live file must keep accepting writes, got %v", err)
	}
}
