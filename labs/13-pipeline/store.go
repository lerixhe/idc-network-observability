package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 纯文件分段存储：一个窗口一个 JSONL 段，段内按 (probe_id, metric_id) 排序。
//
// 为什么不用 SQLite：这个实验只需要三件事——分段、保留期、回放重写，
// 文件目录能直接看见段的存在与消失，加一个 SQLite 驱动就把「零依赖」这条丢了。
// 代价要说清：没有索引、没有事务、查询靠扫段。真实的时序库两头都不占，见第 34 课。
//
// 段的命名带窗口起点（w<unix>.jsonl），保留期的判定不需要打开文件，
// 删段就是删文件——这也是 TTL 在文件系统上的原始形态。

const (
	tier1m = "agg-1m"
	tier5m = "agg-5m"
)

// Store 是一个分层的目录树：<root>/<tier>/w<unix>.jsonl。
type Store struct {
	root string
}

func NewStore(root string) Store { return Store{root: root} }

// TierStat 是一层存储的体检数字。Digest 覆盖全部段的内容，
// 用它判「回放重写之后落盘结果有没有变」比逐字段对比省事，也比只比字节数靠谱。
type TierStat struct {
	Tier     string
	Segments int
	Aggs     int
	Bytes    int64
	Digest   string
}

// PruneStat 是一次保留期清理的结果。
type PruneStat struct {
	Tier       string
	Retention  time.Duration
	Removed    int
	FreedBytes int64
	Kept       int
}

// Write 把聚合按窗口分段落盘。同一窗口重复写会覆盖成同样的内容——
// 这是回放安全的前提：重放一遍，段不变，digest 不变。
func (s Store) Write(tier string, aggs []Agg) (TierStat, error) {
	dir := filepath.Join(s.root, tier)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return TierStat{}, err
	}
	byWindow := make(map[int64][]Agg, len(aggs))
	for _, a := range aggs {
		ws := a.WindowStart.Unix()
		byWindow[ws] = append(byWindow[ws], a)
	}
	for ws, list := range byWindow {
		sortAggs(list)
		path := filepath.Join(dir, fmt.Sprintf("w%d.jsonl", ws))
		f, err := os.Create(path)
		if err != nil {
			return TierStat{}, err
		}
		enc := json.NewEncoder(f)
		enc.SetEscapeHTML(false)
		for _, a := range list {
			if err := enc.Encode(a); err != nil {
				f.Close()
				return TierStat{}, err
			}
		}
		if err := f.Close(); err != nil {
			return TierStat{}, err
		}
	}
	return s.Stat(tier)
}

// Stat 数段、数点、算 digest。digest 按文件名排序后逐段吃进 sha256，
// 内容与段边界任一变化都会变。
func (s Store) Stat(tier string) (TierStat, error) {
	stat := TierStat{Tier: tier}
	dir := filepath.Join(s.root, tier)
	names, err := segmentNames(dir)
	if err != nil {
		return stat, err
	}
	h := sha256.New()
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return stat, err
		}
		stat.Segments++
		stat.Bytes += int64(len(data))
		stat.Aggs += countJSONLines(data)
		h.Write([]byte(name))
		h.Write(data)
	}
	stat.Digest = hex.EncodeToString(h.Sum(nil))[:16]
	return stat, nil
}

// Prune 删掉「窗口起点 + 保留期 ≤ now」的段。返回删了几个、腾了多少字节。
func (s Store) Prune(tier string, now time.Time, retention time.Duration) (PruneStat, error) {
	stat := PruneStat{Tier: tier, Retention: retention}
	dir := filepath.Join(s.root, tier)
	names, err := segmentNames(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return stat, nil
		}
		return stat, err
	}
	for _, name := range names {
		ws, ok := parseSegmentName(name)
		if !ok {
			continue
		}
		if time.Unix(ws, 0).UTC().Add(retention).After(now) {
			stat.Kept++
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			return stat, err
		}
		if err := os.Remove(path); err != nil {
			return stat, err
		}
		stat.Removed++
		stat.FreedBytes += info.Size()
	}
	return stat, nil
}

// Load 把一层读回内存。段之间按窗口排序，段内保持落盘顺序。
func (s Store) Load(tier string) ([]Agg, error) {
	dir := filepath.Join(s.root, tier)
	names, err := segmentNames(dir)
	if err != nil {
		return nil, err
	}
	var out []Agg
	for _, name := range names {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var a Agg
			if err := json.Unmarshal([]byte(line), &a); err != nil {
				f.Close()
				return nil, err
			}
			a.WindowStart = a.WindowStart.UTC()
			out = append(out, a)
		}
		if err := sc.Err(); err != nil {
			f.Close()
			return nil, err
		}
		f.Close()
	}
	return out, nil
}

// segmentNames 返回目录里的段名，按窗口起点升序。
func segmentNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Slice(names, func(i, j int) bool {
		a, _ := parseSegmentName(names[i])
		b, _ := parseSegmentName(names[j])
		return a < b
	})
	return names, nil
}

func parseSegmentName(name string) (int64, bool) {
	if !strings.HasPrefix(name, "w") || !strings.HasSuffix(name, ".jsonl") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(name, "w"), ".jsonl"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func countJSONLines(data []byte) int {
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	return n
}
