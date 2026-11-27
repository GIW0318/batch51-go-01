package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-a-25"
)

func codexConfig() impl.CodexConfig {
	return impl.CodexConfig{
		MaxEntriesPerAccount: 64,
		MaxEvents:            64,
		MaxDailyConvert:      10,
		ConvertRate:          2,
	}
}

func newCodex(t *testing.T) *impl.CollectionCodexLedger {
	t.Helper()
	return impl.NewCollectionCodexLedger(codexConfig())
}

func regEntry(t *testing.T, l *impl.CollectionCodexLedger, id, name string) {
	t.Helper()
	if err := l.RegisterEntry(impl.CatalogEntry{ID: id, Name: name}); err != nil {
		t.Fatalf("register entry %s: %v", id, err)
	}
}

func regSet(t *testing.T, l *impl.CollectionCodexLedger, id string, ids []string, reward string, amount int) {
	t.Helper()
	if err := l.RegisterSet(impl.EntrySet{ID: id, EntryIDs: ids, RewardID: reward, RewardAmount: amount}); err != nil {
		t.Fatalf("register set %s: %v", id, err)
	}
}

func discoverOne(t *testing.T, l *impl.CollectionCodexLedger, acct, eventID, entryID string) impl.DiscoverResult {
	t.Helper()
	r, err := l.Discover(acct, impl.DiscoverEvent{EventID: eventID, EntryID: entryID, Qty: 1})
	if err != nil {
		t.Fatalf("discover %s as %s: %v", entryID, eventID, err)
	}
	return r
}

func TestRegisterEntryRejectsBadEntry(t *testing.T) {
	l := newCodex(t)
	if err := l.RegisterEntry(impl.CatalogEntry{ID: "  ", Name: "coin"}); !errors.Is(err, impl.ErrBadEntry) {
		t.Fatalf("blank entry id = %v, want ErrBadEntry", err)
	}
	if err := l.RegisterEntry(impl.CatalogEntry{ID: "e1", Name: "  "}); !errors.Is(err, impl.ErrBadEntry) {
		t.Fatalf("blank entry name = %v, want ErrBadEntry", err)
	}
	regEntry(t, l, "e1", "Copper Coin")
	if err := l.RegisterEntry(impl.CatalogEntry{ID: "e1", Name: "Copper Coin"}); !errors.Is(err, impl.ErrDuplicateEntry) {
		t.Fatalf("duplicate entry = %v, want ErrDuplicateEntry", err)
	}
	if stats := l.Stats(); stats.Entries != 1 {
		t.Fatalf("entry count = %d, want 1", stats.Entries)
	}
}

func TestRegisterSetRejectsBadSet(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	regEntry(t, l, "e2", "Silver Coin")
	if err := l.RegisterSet(impl.EntrySet{ID: "s1", EntryIDs: []string{"ghost"}, RewardID: "chest", RewardAmount: 10}); !errors.Is(err, impl.ErrUnknownEntry) {
		t.Fatalf("set with an unknown entry = %v, want ErrUnknownEntry", err)
	}
	if err := l.RegisterSet(impl.EntrySet{ID: "s1", EntryIDs: nil, RewardID: "chest", RewardAmount: 10}); !errors.Is(err, impl.ErrBadSet) {
		t.Fatalf("set without entries = %v, want ErrBadSet", err)
	}
	regSet(t, l, "s1", []string{"e1", "e2"}, "chest", 100)
	if err := l.RegisterSet(impl.EntrySet{ID: "s1", EntryIDs: []string{"e1"}, RewardID: "chest", RewardAmount: 1}); !errors.Is(err, impl.ErrDuplicateSet) {
		t.Fatalf("duplicate set = %v, want ErrDuplicateSet", err)
	}
}

func TestDiscoverAddsCountAndFlagsDuplicate(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	first := discoverOne(t, l, "p1", "v1", "e1")
	if first.Count != 1 || first.Duplicate {
		t.Fatalf("first discovery = %+v, want count 1 without a duplicate flag", first)
	}
	second := discoverOne(t, l, "p1", "v2", "e1")
	if second.Count != 2 || !second.Duplicate {
		t.Fatalf("second discovery = %+v, want count 2 flagged as a duplicate", second)
	}
}

func TestDiscoverIsIdempotentByEventID(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	discoverOne(t, l, "p1", "v1", "e1")
	again := discoverOne(t, l, "p1", "v1", "e1")
	if !again.Replayed {
		t.Fatalf("replayed event was not marked: %+v", again)
	}
	if again.Count != 1 {
		t.Fatalf("count after the replay = %d, want 1", again.Count)
	}
	if stats := l.Stats(); stats.Discovered != 1 {
		t.Fatalf("discovered counter = %d, want 1", stats.Discovered)
	}
}

func TestDiscoverRejectsUnknownEntryAndBadEvent(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	if _, err := l.Discover("p1", impl.DiscoverEvent{EventID: "v1", EntryID: "ghost", Qty: 1}); !errors.Is(err, impl.ErrUnknownEntry) {
		t.Fatalf("unknown entry = %v, want ErrUnknownEntry", err)
	}
	if _, err := l.Discover("p1", impl.DiscoverEvent{EventID: "  ", EntryID: "e1", Qty: 1}); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("blank event id = %v, want ErrBadEvent", err)
	}
	if _, err := l.Discover("  ", impl.DiscoverEvent{EventID: "v1", EntryID: "e1", Qty: 1}); !errors.Is(err, impl.ErrBadAccount) {
		t.Fatalf("blank account = %v, want ErrBadAccount", err)
	}
	if _, err := l.Discover("p1", impl.DiscoverEvent{EventID: "v1", EntryID: "e1", Qty: 0}); !errors.Is(err, impl.ErrBadQty) {
		t.Fatalf("zero quantity = %v, want ErrBadQty", err)
	}
}

func TestUnlockVariantIsIdempotent(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	discoverOne(t, l, "p1", "v1", "e1")
	if err := l.UnlockVariant("p1", "e1", "gold"); err != nil {
		t.Fatalf("unlock gold variant: %v", err)
	}
	if err := l.UnlockVariant("p1", "e1", "gold"); err != nil {
		t.Fatalf("unlock the same variant again = %v, want nil", err)
	}
	if stats := l.Stats(); stats.Variants != 1 {
		t.Fatalf("variant counter = %d, want 1", stats.Variants)
	}
	if err := l.UnlockVariant("p1", "ghost", "gold"); !errors.Is(err, impl.ErrUnknownEntry) {
		t.Fatalf("variant of an unknown entry = %v, want ErrUnknownEntry", err)
	}
}

func TestClaimSetAwardsOnce(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	regEntry(t, l, "e2", "Silver Coin")
	regSet(t, l, "s1", []string{"e1", "e2"}, "chest", 100)
	first := discoverOne(t, l, "p1", "v1", "e1")
	if len(first.CompletedSets) != 0 {
		t.Fatalf("half finished set reported complete: %+v", first.CompletedSets)
	}
	second := discoverOne(t, l, "p1", "v2", "e2")
	if len(second.CompletedSets) != 1 || second.CompletedSets[0] != "s1" {
		t.Fatalf("completion signal = %+v, want s1", second.CompletedSets)
	}
	reward, err := l.ClaimSet("p1", "s1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if reward.RewardID != "chest" || reward.Amount != 100 || reward.AlreadyClaimed {
		t.Fatalf("reward = %+v, want a fresh chest of 100", reward)
	}
	if len(reward.Missing) != 0 {
		t.Fatalf("missing entries = %+v, want none", reward.Missing)
	}
	if _, err := l.ClaimSet("p1", "s1"); !errors.Is(err, impl.ErrAlreadyClaimed) {
		t.Fatalf("second claim = %v, want ErrAlreadyClaimed", err)
	}
	stats := l.Stats()
	if stats.SetsCompleted != 1 || stats.SetsClaimed != 1 {
		t.Fatalf("stats = %+v, want one completed and one claimed set", stats)
	}
}

func TestClaimSetReportsMissing(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	regEntry(t, l, "e2", "Silver Coin")
	regSet(t, l, "s1", []string{"e1", "e2"}, "chest", 100)
	discoverOne(t, l, "p1", "v1", "e1")
	partial, err := l.ClaimSet("p1", "s1")
	if !errors.Is(err, impl.ErrSetIncomplete) {
		t.Fatalf("claim of a half finished set = %v, want ErrSetIncomplete", err)
	}
	if len(partial.Missing) != 1 || partial.Missing[0] != "e2" {
		t.Fatalf("missing = %+v, want e2", partial.Missing)
	}
	if _, err := l.ClaimSet("p1", "ghost"); !errors.Is(err, impl.ErrUnknownSet) {
		t.Fatalf("claim of an unknown set = %v, want ErrUnknownSet", err)
	}
}

func TestConvertDuplicateRequiresSurplus(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	discoverOne(t, l, "p1", "v1", "e1")
	if _, err := l.ConvertDuplicate("p1", "e1", 1); !errors.Is(err, impl.ErrNoDuplicate) {
		t.Fatalf("conversion without a surplus copy = %v, want ErrNoDuplicate", err)
	}
	discoverOne(t, l, "p1", "v2", "e1")
	if _, err := l.ConvertDuplicate("p1", "e1", 5); !errors.Is(err, impl.ErrBadQty) {
		t.Fatalf("conversion of five copies out of two = %v, want ErrBadQty", err)
	}
	res, err := l.ConvertDuplicate("p1", "e1", 1)
	if err != nil {
		t.Fatalf("conversion: %v", err)
	}
	if res.Consumed != 1 || res.Produced != 2 {
		t.Fatalf("conversion = %+v, want one copy turned into two shards", res)
	}
	if res.DailyUsed != 1 || res.DailyLimit != 10 {
		t.Fatalf("daily counters = %d/%d, want 1/10", res.DailyUsed, res.DailyLimit)
	}
	if stats := l.Stats(); stats.Converted != 1 {
		t.Fatalf("converted counter = %d, want 1", stats.Converted)
	}
}

func TestConvertDailyLimit(t *testing.T) {
	cfg := codexConfig()
	cfg.MaxDailyConvert = 2
	l := impl.NewCollectionCodexLedger(cfg)
	regEntry(t, l, "e1", "Copper Coin")
	for i, event := range []string{"v1", "v2", "v3", "v4", "v5"} {
		if _, err := l.Discover("p1", impl.DiscoverEvent{EventID: event, EntryID: "e1", Qty: 1}); err != nil {
			t.Fatalf("discovery %d: %v", i+1, err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := l.ConvertDuplicate("p1", "e1", 1); err != nil {
			t.Fatalf("conversion %d: %v", i+1, err)
		}
	}
	if _, err := l.ConvertDuplicate("p1", "e1", 1); !errors.Is(err, impl.ErrDailyLimit) {
		t.Fatalf("third conversion = %v, want ErrDailyLimit", err)
	}
}

func TestExplainListsReasons(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	discoverOne(t, l, "p1", "v1", "e1")
	if err := l.UnlockVariant("p1", "e1", "gold"); err != nil {
		t.Fatalf("unlock variant: %v", err)
	}
	reasons := l.Explain("p1", "e1")
	kinds := make(map[string]bool, len(reasons))
	for _, r := range reasons {
		kinds[r.Kind] = true
	}
	if !kinds["discovered"] {
		t.Fatalf("reasons = %+v, want a discovered reason", reasons)
	}
	if !kinds["variant"] {
		t.Fatalf("reasons = %+v, want a variant reason", reasons)
	}
}

func TestStatsCountsCodex(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	regEntry(t, l, "e2", "Silver Coin")
	regSet(t, l, "s1", []string{"e1", "e2"}, "chest", 100)
	discoverOne(t, l, "p1", "v1", "e1")
	discoverOne(t, l, "p1", "v2", "e2")
	if _, err := l.ClaimSet("p1", "s1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	stats := l.Stats()
	if stats.Entries != 2 || stats.Sets != 1 || stats.Accounts != 1 {
		t.Fatalf("stats = %+v, want two entries and one set on one account", stats)
	}
	if stats.Discovered != 2 || stats.SetsCompleted != 1 || stats.SetsClaimed != 1 {
		t.Fatalf("stats = %+v, want two discoveries and one claimed set", stats)
	}
}

func TestMigrateRejectsUnknownVersion(t *testing.T) {
	l := newCodex(t)
	if err := l.Migrate("p1", "v1", "v2"); !errors.Is(err, impl.ErrUnknownVersion) {
		t.Fatalf("migration between unknown versions = %v, want ErrUnknownVersion", err)
	}
	if err := l.RegisterVersion(impl.VersionSpec{ID: "v1", Order: 1}); err != nil {
		t.Fatalf("register v1: %v", err)
	}
	if err := l.RegisterVersion(impl.VersionSpec{ID: "v2", Order: 2}); err != nil {
		t.Fatalf("register v2: %v", err)
	}
	if err := l.Migrate("p1", "v2", "v1"); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("backwards migration = %v, want ErrBadVersion", err)
	}
	if err := l.Migrate("p1", "v1", "v2"); err != nil {
		t.Fatalf("forward migration = %v, want nil", err)
	}
}

func TestSnapshotRestoreKeepsCodex(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	regEntry(t, l, "e2", "Silver Coin")
	regSet(t, l, "s1", []string{"e1", "e2"}, "chest", 100)
	discoverOne(t, l, "p1", "v1", "e1")
	discoverOne(t, l, "p1", "v2", "e2")
	if _, err := l.ClaimSet("p1", "s1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	data, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewCollectionCodexLedger(codexConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	stats := restored.Stats()
	if stats.Entries != 2 || stats.Sets != 1 || stats.Discovered != 2 || stats.SetsClaimed != 1 {
		t.Fatalf("restored stats = %+v, want the catalog and the claimed set back", stats)
	}
	if _, err := restored.ClaimSet("p1", "s1"); !errors.Is(err, impl.ErrAlreadyClaimed) {
		t.Fatalf("claim after restore = %v, want ErrAlreadyClaimed", err)
	}
	if err := restored.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload = %v, want ErrBadSnapshot", err)
	}
	if stats := restored.Stats(); stats.Discovered != 2 {
		t.Fatalf("stats after a failed restore = %+v, want the previous state", stats)
	}
}

func TestDiscoverUnlocksVariantOnFirstEvent(t *testing.T) {
	l := newCodex(t)
	regEntry(t, l, "e1", "Copper Coin")
	r, err := l.Discover("p1", impl.DiscoverEvent{EventID: "v1", EntryID: "e1", Variant: "gold", Qty: 1})
	if err != nil {
		t.Fatalf("discover with a variant: %v", err)
	}
	if !r.VariantUnlocked {
		t.Fatalf("first event with a variant did not unlock it: %+v", r)
	}
	plain := discoverOne(t, l, "p1", "v2", "e1")
	if plain.VariantUnlocked {
		t.Fatalf("an event without a variant unlocked something: %+v", plain)
	}
	if stats := l.Stats(); stats.Variants != 1 {
		t.Fatalf("variant counter = %d, want 1", stats.Variants)
	}
}
