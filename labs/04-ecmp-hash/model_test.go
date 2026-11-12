package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

// sampleTuple 是测试里用的固定五元组。改它的任何一个字段，落桶结果都会变，
// 所以相关的 golden 断言跟着它一起改。
func sampleTuple() Tuple {
	return Tuple{
		SrcIP:   netip.MustParseAddr("10.20.3.11"),
		DstIP:   netip.MustParseAddr("10.20.9.7"),
		Proto:   6,
		SrcPort: 40001,
		DstPort: 5021,
	}
}

// TestEncodeTupleLayout 把哈希输入的字节口径钉死。
//
// 字段顺序一变，全部落桶结果跟着变，而且没有任何报错——这类改动只能靠测试拦。
func TestEncodeTupleLayout(t *testing.T) {
	got := sampleTuple().encode()
	want := []byte{
		10, 20, 3, 11, // 源 IP
		10, 20, 9, 7, // 目的 IP
		6,          // 协议号
		0x9c, 0x41, // 源端口 40001
		0x13, 0x9d, // 目的端口 5021
	}
	if len(got) != 13 {
		t.Fatalf("编码应当是 13 字节，实际 %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 字节 = %#x, 期望 %#x（完整：%x）", i, got[i], want[i], got)
		}
	}
}

func TestTupleString(t *testing.T) {
	got := sampleTuple().String()
	want := "10.20.3.11:40001 → 10.20.9.7:5021/6"
	if got != want {
		t.Errorf("Tuple.String() = %q, 期望 %q", got, want)
	}
}

func TestLoadPathSetsFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paths.json")
	content := `[
	  {"name":"sw-a → sw-b","route":"10.0.0.0/24","hash_salt":305419896,
	   "base":{"src_ip":"10.0.1.1","dst_ip":"10.0.2.2","proto":6,"dst_port":443},
	   "members":[{"id":"m0","desc":"a"},{"id":"m1","desc":"b"}]}
	]`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sets, err := loadPathSets(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 {
		t.Fatalf("应当解析出 1 组，实际 %d", len(sets))
	}
	s := sets[0]
	if s.Name != "sw-a → sw-b" || s.Route != "10.0.0.0/24" || s.HashSalt != 305419896 {
		t.Errorf("头部字段解析不对: %+v", s)
	}
	if s.Base.SrcIP.String() != "10.0.1.1" || s.Base.DstPort != 443 || s.Base.Proto != 6 {
		t.Errorf("base 四元组解析不对: %+v", s.Base)
	}
	if n := len(s.Members); n != 2 {
		t.Errorf("member 数应当是 2，实际 %d", n)
	}
}

func TestTupleWithPortKeepsBase(t *testing.T) {
	set := PathSet{Base: sampleTuple()}
	got := set.TupleWithPort(50000)
	if got.SrcPort != 50000 {
		t.Errorf("源端口应当被覆盖成 50000，实际 %d", got.SrcPort)
	}
	if got.SrcIP != set.Base.SrcIP || got.DstIP != set.Base.DstIP ||
		got.Proto != set.Base.Proto || got.DstPort != set.Base.DstPort {
		t.Errorf("其余四个字段不该被动过: %+v", got)
	}
}

// TestLoadPathSetsValidation 逐个打穿校验分支。
// 样本脏了还硬跑，后面三张表全是假数据，而且看起来完全正常。
func TestLoadPathSetsValidation(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"空数组", `[]`},
		{"src 不是 IP", `[{"name":"x","base":{"src_ip":"10.0.1","dst_ip":"10.0.2.2","proto":6,"dst_port":80},
			"members":[{"id":"m0"},{"id":"m1"}]}]`},
		{"IPv6 不收", `[{"name":"x","base":{"src_ip":"2001:db8::1","dst_ip":"10.0.2.2","proto":6,"dst_port":80},
			"members":[{"id":"m0"},{"id":"m1"}]}]`},
		{"proto 越界", `[{"name":"x","base":{"src_ip":"10.0.1.1","dst_ip":"10.0.2.2","proto":300,"dst_port":80},
			"members":[{"id":"m0"},{"id":"m1"}]}]`},
		{"dst_port 为 0", `[{"name":"x","base":{"src_ip":"10.0.1.1","dst_ip":"10.0.2.2","proto":6,"dst_port":0},
			"members":[{"id":"m0"},{"id":"m1"}]}]`},
		{"只有一条 member", `[{"name":"x","base":{"src_ip":"10.0.1.1","dst_ip":"10.0.2.2","proto":6,"dst_port":80},
			"members":[{"id":"m0"}]}]`},
		{"member id 重复", `[{"name":"x","base":{"src_ip":"10.0.1.1","dst_ip":"10.0.2.2","proto":6,"dst_port":80},
			"members":[{"id":"m0"},{"id":"m0"}]}]`},
		{"member 没 id", `[{"name":"x","base":{"src_ip":"10.0.1.1","dst_ip":"10.0.2.2","proto":6,"dst_port":80},
			"members":[{"id":""},{"id":"m1"}]}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "paths.json")
			if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := loadPathSets(path); err == nil {
				t.Error("应当报错，不能静默放过")
			}
		})
	}
}

// TestSamplesLoadable 保证仓库里的样本能被本版本的代码读进来。
func TestSamplesLoadable(t *testing.T) {
	sets, err := loadPathSets("samples/paths.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 2 {
		t.Fatalf("样本应当是 2 组等价路径，实际 %d", len(sets))
	}
	if n := len(sets[0].Members); n != 4 {
		t.Errorf("第一组应当是 4 条 member，实际 %d", n)
	}
	// 第二组故意是 6 条：不是 2 的幂，pow2 落桶会露出 2:1 的负载偏差。
	if n := len(sets[1].Members); n != 6 {
		t.Errorf("第二组应当是 6 条 member，实际 %d", n)
	}
	if sets[0].HashSalt == sets[1].HashSalt {
		t.Error("两组的哈希盐应当不同，否则演示不出「换设备落桶全变」")
	}
}
