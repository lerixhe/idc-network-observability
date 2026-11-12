package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleAggs(t *testing.T) []Agg {
	t.Helper()
	stream := sampleStream(t)
	return runWindow(stream, time.Minute, 15*time.Minute, 15*time.Minute).Aggs
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	aggs := sampleAggs(t)

	stat, err := st.Write(tier1m, aggs)
	if err != nil {
		t.Fatalf("写段: %v", err)
	}
	if stat.Segments == 0 || stat.Aggs != len(aggs) {
		t.Fatalf("段数 %d / 聚合数 %d，期望 %d 个聚合", stat.Segments, stat.Aggs, len(aggs))
	}

	back, err := st.Load(tier1m)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if len(back) != len(aggs) {
		t.Fatalf("读回 %d 个聚合 ≠ 写入 %d 个", len(back), len(aggs))
	}
	if aggsDigest(back) != aggsDigest(aggs) {
		t.Fatal("读回的聚合与写入的不一致")
	}
}

// 回放安全：同一批聚合再写一遍，段内容与 digest 都不变。
func TestStoreRewriteIsIdempotent(t *testing.T) {
	st := NewStore(t.TempDir())
	aggs := sampleAggs(t)

	first, err := st.Write(tier1m, aggs)
	if err != nil {
		t.Fatalf("首写: %v", err)
	}
	second, err := st.Write(tier1m, aggs)
	if err != nil {
		t.Fatalf("重写: %v", err)
	}
	if first.Digest != second.Digest || first.Bytes != second.Bytes || first.Segments != second.Segments {
		t.Fatalf("重写后段变了：%+v → %+v", first, second)
	}
}

func TestStorePruneByRetention(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	aggs := sampleAggs(t)
	if _, err := st.Write(tier1m, aggs); err != nil {
		t.Fatalf("写段: %v", err)
	}
	stat, err := st.Stat(tier1m)
	if err != nil {
		t.Fatalf("统计: %v", err)
	}

	// 保留期内：一段都不该删。样本跨度 30 分钟，留一天够用。
	kept, err := st.Prune(tier1m, latestWindow(aggs).Add(time.Hour), 24*time.Hour)
	if err != nil {
		t.Fatalf("保留期内的清理: %v", err)
	}
	if kept.Removed != 0 {
		t.Fatalf("还在保留期内却删了 %d 段", kept.Removed)
	}

	// 越过保留期：整层过期，段全删，字节数要报出来。
	removed, err := st.Prune(tier1m, latestWindow(aggs).Add(48*time.Hour), 24*time.Hour)
	if err != nil {
		t.Fatalf("过期清理: %v", err)
	}
	if removed.Removed != stat.Segments {
		t.Fatalf("过期应删 %d 段，实际 %d", stat.Segments, removed.Removed)
	}
	if removed.FreedBytes <= 0 {
		t.Fatal("腾出的字节数应大于 0")
	}
	if removed.Kept != 0 {
		t.Fatalf("整层过期后不该有残留段，实际 %d", removed.Kept)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, tier1m)); err != nil || len(entries) != 0 {
		t.Fatalf("目录里还有文件：%v %d", err, len(entries))
	}
}

func TestStorePruneOnMissingTierIsNoop(t *testing.T) {
	st := NewStore(t.TempDir())
	stat, err := st.Prune(tier5m, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("目录不存在时应无操作，实际报错: %v", err)
	}
	if stat.Removed != 0 {
		t.Fatalf("删了 %d 段", stat.Removed)
	}
}

func latestWindow(aggs []Agg) time.Time {
	var latest time.Time
	for _, a := range aggs {
		if a.WindowStart.After(latest) {
			latest = a.WindowStart
		}
	}
	return latest
}

func TestRollup5mWrittenToOwnTier(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	aggs := sampleAggs(t)
	if _, err := st.Write(tier1m, aggs); err != nil {
		t.Fatalf("写 1 分钟层: %v", err)
	}
	rolled := rollup5m(aggs)
	stat, err := st.Write(tier5m, rolled)
	if err != nil {
		t.Fatalf("写 5 分钟层: %v", err)
	}
	if stat.Aggs != len(rolled) {
		t.Fatalf("5 分钟层 %d 个聚合 ≠ %d", stat.Aggs, len(rolled))
	}
	back, err := st.Load(tier5m)
	if err != nil {
		t.Fatalf("读 5 分钟层: %v", err)
	}
	if aggsDigest(back) != aggsDigest(rolled) {
		t.Fatal("5 分钟层读回不一致")
	}
}
