package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-c-05"
)

func baseAsset() impl.Asset {
	return impl.Asset{
		ID:              "asset-1",
		Tenant:          "tenant-a",
		Width:           3840,
		Height:          2160,
		FPSNum:          30000,
		FPSDen:          1001,
		TimebaseNum:     90000,
		TimebaseDen:     1,
		StartTimecodeMS: 86400000,
		DurationMS:      60000,
		ContentHash:     "sha256-content-a",
		MetadataHash:    "sha256-meta-a",
		Bytes:           4 << 30,
	}
}

func planned(t *testing.T, g *impl.ProxyGenerator, asset impl.Asset) impl.ProxyPlan {
	t.Helper()
	plan, err := g.Plan(asset)
	if err != nil {
		t.Fatalf("plan must succeed, got %v", err)
	}
	return plan
}

func doneRange(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func TestNewGeneratorDefaults(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	limits := g.Limits()
	if limits.MaxConcurrent != impl.DefaultMaxConcurrent {
		t.Fatalf("default concurrency = %d, want %d", limits.MaxConcurrent, impl.DefaultMaxConcurrent)
	}
	if limits.DiskBudgetBytes != impl.DefaultDiskBudget {
		t.Fatalf("default disk budget = %d, want %d", limits.DiskBudgetBytes, impl.DefaultDiskBudget)
	}
	if limits.MaxShards != impl.DefaultMaxShards {
		t.Fatalf("default shard limit = %d, want %d", limits.MaxShards, impl.DefaultMaxShards)
	}
	if limits.ShardMS != impl.DefaultShardMS {
		t.Fatalf("default shard length = %d, want %d", limits.ShardMS, impl.DefaultShardMS)
	}
}

func TestPlanSelectsTierByHeight(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	cases := []struct {
		height int
		tier   string
		out    int
	}{
		{2160, impl.TierHigh, 1080},
		{1080, impl.TierHigh, 1080},
		{720, impl.TierMid, 720},
		{480, impl.TierLow, 480},
	}
	for _, c := range cases {
		asset := baseAsset()
		asset.Height = c.height
		plan := planned(t, g, asset)
		if plan.Tier != c.tier {
			t.Fatalf("height %d tier = %q, want %q", c.height, plan.Tier, c.tier)
		}
		if plan.Height != c.out {
			t.Fatalf("height %d proxy height = %d, want %d", c.height, plan.Height, c.out)
		}
	}
}

func TestPlanKeepsTimecodeOffset(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	plan := planned(t, g, baseAsset())
	if plan.TimecodeOffsetMS != 86400000 {
		t.Fatalf("timecode offset = %d, want 86400000", plan.TimecodeOffsetMS)
	}
	zero := baseAsset()
	zero.StartTimecodeMS = 0
	plan = planned(t, g, zero)
	if plan.TimecodeOffsetMS != 0 {
		t.Fatalf("zero timecode offset = %d, want 0", plan.TimecodeOffsetMS)
	}
	if plan.AssetID != zero.ID || plan.Tenant != zero.Tenant {
		t.Fatalf("plan identity = %q/%q, want %q/%q", plan.AssetID, plan.Tenant, zero.ID, zero.Tenant)
	}
}

func TestPlanComputesShards(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	n, err := g.Shards(baseAsset())
	if err != nil {
		t.Fatalf("shard count must succeed, got %v", err)
	}
	if n != 6 {
		t.Fatalf("shard count for sixty seconds = %d, want 6", n)
	}
	plan := planned(t, g, baseAsset())
	if plan.Shards != 6 || plan.ShardMS != 10000 {
		t.Fatalf("plan shards=%d length=%d, want 6 and 10000", plan.Shards, plan.ShardMS)
	}
	odd := baseAsset()
	odd.DurationMS = 60001
	if n, err := g.Shards(odd); err != nil || n != 7 {
		t.Fatalf("shard count for a partial tail = %d err=%v, want 7 and nil", n, err)
	}
}

func TestPlanRejectsBadAsset(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	cases := []impl.Asset{}
	emptyID := baseAsset()
	emptyID.ID = ""
	cases = append(cases, emptyID)
	emptyTenant := baseAsset()
	emptyTenant.Tenant = ""
	cases = append(cases, emptyTenant)
	zeroHeight := baseAsset()
	zeroHeight.Height = 0
	cases = append(cases, zeroHeight)
	zeroDuration := baseAsset()
	zeroDuration.DurationMS = 0
	cases = append(cases, zeroDuration)
	badFPS := baseAsset()
	badFPS.FPSDen = 0
	cases = append(cases, badFPS)
	badTimebase := baseAsset()
	badTimebase.TimebaseNum = 0
	cases = append(cases, badTimebase)
	emptyHash := baseAsset()
	emptyHash.ContentHash = ""
	cases = append(cases, emptyHash)
	for i, asset := range cases {
		if _, err := g.Plan(asset); !errors.Is(err, impl.ErrBadAsset) {
			t.Fatalf("case %d: err = %v, want ErrBadAsset", i, err)
		}
	}
}

func TestPlanEnforcesShardLimit(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{MaxShards: 2})
	if _, err := g.Plan(baseAsset()); !errors.Is(err, impl.ErrShardLimit) {
		t.Fatalf("plan past the shard limit err = %v, want ErrShardLimit", err)
	}
	if _, err := g.Shards(baseAsset()); !errors.Is(err, impl.ErrShardLimit) {
		t.Fatalf("shard count past the limit err = %v, want ErrShardLimit", err)
	}
	if _, ok := g.ProgressOf("asset-1"); ok {
		t.Fatalf("a rejected plan must not register progress")
	}
}

func TestPlanIsStableAndKeepsProgress(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	asset := baseAsset()
	first := planned(t, g, asset)
	second := planned(t, g, asset)
	if first.ProxyID != second.ProxyID || first.Tier != second.Tier || first.Shards != second.Shards {
		t.Fatalf("replanning changed identity: %+v vs %+v", first, second)
	}
	if first.Params != second.Params {
		t.Fatalf("replanning changed parameters: %q vs %q", first.Params, second.Params)
	}
	if _, err := g.Generate(first, 2); err != nil {
		t.Fatalf("generation must succeed, got %v", err)
	}
	planned(t, g, asset)
	cur, ok := g.ProgressOf(first.ProxyID)
	if !ok {
		t.Fatalf("progress is missing after replanning")
	}
	if cur.Done != 2 {
		t.Fatalf("progress after replanning = %d, want 2", cur.Done)
	}
	if g.Status(first.ProxyID) != impl.StatusRunning {
		t.Fatalf("status after replanning = %q, want %q", g.Status(first.ProxyID), impl.StatusRunning)
	}
}

func TestGenerateAdvancesShards(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	plan := planned(t, g, baseAsset())
	progress, err := g.Generate(plan, 2)
	if err != nil {
		t.Fatalf("generation must succeed, got %v", err)
	}
	if progress.Done != 2 || progress.Total != 6 {
		t.Fatalf("progress done=%d total=%d, want 2 and 6", progress.Done, progress.Total)
	}
	if progress.ProxyID != plan.ProxyID {
		t.Fatalf("progress proxy id = %q, want %q", progress.ProxyID, plan.ProxyID)
	}
	if progress.Suspended {
		t.Fatalf("generation suspended with an ample disk budget")
	}
	if progress.DiskBytes <= 0 {
		t.Fatalf("written bytes = %d, want a positive figure", progress.DiskBytes)
	}
	progress, err = g.Generate(plan, 10)
	if err != nil {
		t.Fatalf("second generation must succeed, got %v", err)
	}
	if progress.Done != 6 {
		t.Fatalf("progress after a large budget = %d, want 6", progress.Done)
	}
	progress, err = g.Generate(plan, 4)
	if err != nil {
		t.Fatalf("generating a finished proxy must succeed, got %v", err)
	}
	if progress.Done != 6 || progress.Total != 6 {
		t.Fatalf("progress on a finished proxy done=%d total=%d, want 6 and 6", progress.Done, progress.Total)
	}
}

func TestGenerateRejectsBadBudgetOrPlan(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	plan := planned(t, g, baseAsset())
	if _, err := g.Generate(plan, 0); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("zero budget err = %v, want ErrBadBudget", err)
	}
	if _, err := g.Generate(plan, -2); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("negative budget err = %v, want ErrBadBudget", err)
	}
	if _, err := g.Generate(impl.ProxyPlan{}, 2); !errors.Is(err, impl.ErrBadPlan) {
		t.Fatalf("empty plan err = %v, want ErrBadPlan", err)
	}
	if _, err := g.Generate(impl.ProxyPlan{ProxyID: "x", AssetID: "x", Shards: 0}, 2); !errors.Is(err, impl.ErrBadPlan) {
		t.Fatalf("plan without shards err = %v, want ErrBadPlan", err)
	}
	if g.Status(plan.ProxyID) != impl.StatusPending {
		t.Fatalf("status after rejected calls = %q, want %q", g.Status(plan.ProxyID), impl.StatusPending)
	}
}

func TestVerifyReportsMissingShardsAndHashMismatch(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	asset := baseAsset()
	plan := planned(t, g, asset)
	ref := impl.ProxyRef{
		ID:               plan.ProxyID,
		AssetID:          asset.ID,
		ShardsTotal:      plan.Shards,
		ShardsDone:       []int{0, 1, 3},
		ContentHash:      asset.ContentHash,
		TimecodeOffsetMS: plan.TimecodeOffsetMS,
	}
	res, err := g.Verify(ref)
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	want := []int{2, 4, 5}
	if len(res.MissingShards) != len(want) {
		t.Fatalf("missing shards = %v, want %v", res.MissingShards, want)
	}
	for i, w := range want {
		if res.MissingShards[i] != w {
			t.Fatalf("missing shard %d = %d, want %d", i, res.MissingShards[i], w)
		}
	}
	if res.OK || !res.ContentMatch || !res.TimecodeOK {
		t.Fatalf("verify result ok=%v content=%v timecode=%v, want false/true/true", res.OK, res.ContentMatch, res.TimecodeOK)
	}
	mismatch := ref
	mismatch.ContentHash = "sha256-other"
	res, err = g.Verify(mismatch)
	if err != nil {
		t.Fatalf("verify with a different hash must still succeed, got %v", err)
	}
	if res.ContentMatch || res.OK {
		t.Fatalf("content match=%v ok=%v for a different hash, want false", res.ContentMatch, res.OK)
	}
	if _, err := g.Verify(impl.ProxyRef{ID: "ghost"}); !errors.Is(err, impl.ErrUnknownProxy) {
		t.Fatalf("unknown proxy err = %v, want ErrUnknownProxy", err)
	}
}

func TestVerifyCompleteProxyOK(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	asset := baseAsset()
	plan := planned(t, g, asset)
	ref := impl.ProxyRef{
		ID:               plan.ProxyID,
		AssetID:          asset.ID,
		ShardsTotal:      plan.Shards,
		ShardsDone:       doneRange(plan.Shards),
		ContentHash:      asset.ContentHash,
		TimecodeOffsetMS: plan.TimecodeOffsetMS,
	}
	res, err := g.Verify(ref)
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if !res.OK || !res.ContentMatch || !res.TimecodeOK {
		t.Fatalf("verify result ok=%v content=%v timecode=%v, want all true", res.OK, res.ContentMatch, res.TimecodeOK)
	}
	if len(res.MissingShards) != 0 {
		t.Fatalf("missing shards = %v, want none", res.MissingShards)
	}
	shifted := ref
	shifted.TimecodeOffsetMS = plan.TimecodeOffsetMS + 1
	res, err = g.Verify(shifted)
	if err != nil {
		t.Fatalf("verify with a shifted timecode must still succeed, got %v", err)
	}
	if res.TimecodeOK || res.OK {
		t.Fatalf("timecode match=%v ok=%v for a shifted offset, want false", res.TimecodeOK, res.OK)
	}
}

func TestResumeSkipsCompletedShards(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	asset := baseAsset()
	plan := planned(t, g, asset)
	ref := impl.ProxyRef{
		ID:               plan.ProxyID,
		AssetID:          asset.ID,
		ShardsTotal:      plan.Shards,
		ShardsDone:       []int{0, 1, 2},
		ContentHash:      asset.ContentHash,
		TimecodeOffsetMS: plan.TimecodeOffsetMS,
	}
	progress, err := g.Resume(ref)
	if err != nil {
		t.Fatalf("resume must succeed, got %v", err)
	}
	if progress.Done != 3 || progress.Total != 6 || progress.Skipped != 3 {
		t.Fatalf("resume progress done=%d total=%d skipped=%d, want 3/6/3", progress.Done, progress.Total, progress.Skipped)
	}
	progress, err = g.Generate(plan, 3)
	if err != nil {
		t.Fatalf("generation after resume must succeed, got %v", err)
	}
	if progress.Done != 6 {
		t.Fatalf("progress after finishing a resumed proxy = %d, want 6", progress.Done)
	}
	if _, err := g.Resume(impl.ProxyRef{ID: "ghost", ShardsTotal: 2}); !errors.Is(err, impl.ErrUnknownProxy) {
		t.Fatalf("resume of an unknown proxy err = %v, want ErrUnknownProxy", err)
	}
}

func TestTenantQuotaLimitsConcurrency(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{MaxConcurrent: 2})
	if err := g.Acquire("tenant-a"); err != nil {
		t.Fatalf("first acquire must succeed, got %v", err)
	}
	if err := g.Acquire("tenant-a"); err != nil {
		t.Fatalf("second acquire must succeed, got %v", err)
	}
	if err := g.Acquire("tenant-a"); !errors.Is(err, impl.ErrTenantQuota) {
		t.Fatalf("third acquire err = %v, want ErrTenantQuota", err)
	}
	if err := g.Acquire("tenant-b"); err != nil {
		t.Fatalf("another tenant must not be blocked, got %v", err)
	}
	if err := g.Release("tenant-a"); err != nil {
		t.Fatalf("release must succeed, got %v", err)
	}
	if err := g.Acquire("tenant-a"); err != nil {
		t.Fatalf("acquire after release must succeed, got %v", err)
	}
}

func TestStatusTransitions(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{})
	plan := planned(t, g, baseAsset())
	if got := g.Status(plan.ProxyID); got != impl.StatusPending {
		t.Fatalf("status after plan = %q, want %q", got, impl.StatusPending)
	}
	if _, err := g.Generate(plan, 1); err != nil {
		t.Fatalf("generation must succeed, got %v", err)
	}
	if got := g.Status(plan.ProxyID); got != impl.StatusRunning {
		t.Fatalf("status after a partial run = %q, want %q", got, impl.StatusRunning)
	}
	if _, err := g.Generate(plan, 100); err != nil {
		t.Fatalf("finishing generation must succeed, got %v", err)
	}
	if got := g.Status(plan.ProxyID); got != impl.StatusComplete {
		t.Fatalf("status after completion = %q, want %q", got, impl.StatusComplete)
	}
	if got := g.Status("ghost"); got != impl.StatusUnknown {
		t.Fatalf("status of an unknown proxy = %q, want %q", got, impl.StatusUnknown)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	g := impl.NewProxyGenerator(impl.ProxyLimits{MaxShards: 64, ShardMS: 10000})
	asset := baseAsset()
	plan := planned(t, g, asset)
	if _, err := g.Generate(plan, 2); err != nil {
		t.Fatalf("generation must succeed, got %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewProxyGenerator(impl.ProxyLimits{MaxShards: 1})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	if restored.Limits().MaxShards != 64 {
		t.Fatalf("restored shard limit = %d, want 64", restored.Limits().MaxShards)
	}
	progress, ok := restored.ProgressOf(plan.ProxyID)
	if !ok {
		t.Fatalf("restored generator lost the proxy progress")
	}
	if progress.Done != 2 || progress.Total != 6 {
		t.Fatalf("restored progress done=%d total=%d, want 2 and 6", progress.Done, progress.Total)
	}
	if got := restored.Status(plan.ProxyID); got != impl.StatusRunning {
		t.Fatalf("restored status = %q, want %q", got, impl.StatusRunning)
	}
	ref := impl.ProxyRef{
		ID:               plan.ProxyID,
		AssetID:          asset.ID,
		ShardsTotal:      plan.Shards,
		ShardsDone:       doneRange(plan.Shards),
		ContentHash:      asset.ContentHash,
		TimecodeOffsetMS: plan.TimecodeOffsetMS,
	}
	if res, err := restored.Verify(ref); err != nil || !res.OK {
		t.Fatalf("verify after restore ok=%v err=%v, want true and nil", res.OK, err)
	}
	if err := restored.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot err = %v, want ErrBadSnapshot", err)
	}
	if _, ok := restored.ProgressOf(plan.ProxyID); !ok {
		t.Fatalf("progress lost after a rejected snapshot")
	}
}
