package main

import (
	"testing"
	"time"
)

// UDP 模式存在的意义：真实套接字上的超时与往返，样本模式模拟得再像也不是真的。
// 这里只验一条：回环上能把一列取回来，不可达的那台真的等到超时。
func TestUDPTransportRoundTrip(t *testing.T) {
	f, err := LoadFleet("samples/fleet.json")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := ServeUDP("127.0.0.1:0", f, 1)
	if err != nil {
		t.Fatalf("起 agent 失败: %v", err)
	}
	defer srv.Close()
	cli, err := DialUDP(srv.Addr())
	if err != nil {
		t.Fatalf("连 agent 失败: %v", err)
	}
	defer cli.Close()

	col, _ := ColumnByName("ifName")
	resp, elapsed := cli.Request("spine-01", BulkRequest{Start: col.OID, MaxRepetitions: 64}, 2*time.Second)
	if resp.Error != "" {
		t.Fatalf("回环请求不该出错: %q", resp.Error)
	}
	if len(resp.Varbinds) < 24 {
		t.Fatalf("24 行 ifName，实际拿到 %d 条", len(resp.Varbinds))
	}
	if resp.Varbinds[0].Value.Str != "Ethernet1/1" {
		t.Fatalf("第一条应是 Ethernet1/1，实际 %q", resp.Varbinds[0].Value.Str)
	}
	if elapsed <= 0 {
		t.Fatal("真实套接字上的往返耗时应大于 0")
	}

	// 不可达的设备在回环上就是不回包，客户端只能等到超时。
	resp, elapsed = cli.Request("tor-b7f-01", BulkRequest{Start: col.OID, MaxRepetitions: 16}, 300*time.Millisecond)
	if resp.Error != ErrTimeout {
		t.Fatalf("不可达设备应回 timeout，实际 %q", resp.Error)
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("超时耗时应接近 300ms，实际 %s", elapsed)
	}
}

// 超过设备能力的条数，回环上也走 tooBig 这条路。
func TestUDPTransportTooBig(t *testing.T) {
	f, err := LoadFleet("samples/fleet.json")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := ServeUDP("127.0.0.1:0", f, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli, err := DialUDP(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	col, _ := ColumnByName("ifName")
	resp, _ := cli.Request("tor-a3f-02", BulkRequest{Start: col.OID, MaxRepetitions: 64}, 2*time.Second)
	if resp.Error != ErrTooBig {
		t.Fatalf("C 档设备的 64 条请求应回 tooBig，实际 %q", resp.Error)
	}
}
