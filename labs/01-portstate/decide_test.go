package main

import (
	"strings"
	"testing"
	"time"
)

// goodPort 是一个能一路走到「对端比对」那一步的端口：模块在位、型号认识、
// 收光正常、速率与台账一致。每个用例只改自己要考的那一两个字段。
func goodPort() PortState {
	rx, tx := -6.0, -2.0
	return PortState{
		Device:        "sw-a",
		IfName:        "Ethernet1/1",
		IfIndex:       1,
		IfAdminStatus: adminUp,
		IfOperStatus:  operDown,
		IfSpeedMbps:   10000,
		ExpectMbps:    10000,
		Optic: &Optic{
			Present: true, Model: "SFP-10G-SR",
			RxPowerDbm: &rx, TxPowerDbm: &tx, RxLowAlarmDbm: -14.0,
		},
	}
}

func goodPeer() *PortState {
	p := goodPort()
	p.Device = "sw-b"
	p.IfOperStatus = operUp
	return &p
}

func ptrFloat(v float64) *float64 { return &v }

func TestDecidePort(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PortState)
		peer   func() *PortState
		want   string
		wantIn string
	}{
		{
			name: "本端 admin down 压过模块缺失",
			mutate: func(p *PortState) {
				p.IfAdminStatus = adminDown
				p.Optic = nil
			},
			peer:   func() *PortState { return nil },
			want:   CauseSelfAdminDown,
			wantIn: "ifAdminStatus=2",
		},
		{
			name: "两端都 admin down，记在本端头上",
			mutate: func(p *PortState) {
				p.IfAdminStatus = adminDown
			},
			peer: func() *PortState {
				q := goodPort()
				q.IfAdminStatus = adminDown
				return &q
			},
			want:   CauseSelfAdminDown,
			wantIn: "本端手工 shutdown",
		},
		{
			name: "对端 admin down 且本端无光，不能判成光功率异常",
			mutate: func(p *PortState) {
				p.Optic.RxPowerDbm = ptrFloat(-40)
			},
			peer: func() *PortState {
				q := goodPort()
				q.Device = "sw-b"
				q.IfAdminStatus = adminDown
				return &q
			},
			want:   CausePeerAdminDown,
			wantIn: "sw-b Ethernet1/1",
		},
		{
			name: "对端 admin down 而本端还报 up",
			mutate: func(p *PortState) {
				p.IfOperStatus = operUp
			},
			peer: func() *PortState {
				q := goodPort()
				q.IfAdminStatus = adminDown
				return &q
			},
			want:   CausePeerAdminDown,
			wantIn: "快照有时差",
		},
		{
			name: "快照里没有光模块实体",
			mutate: func(p *PortState) {
				p.Optic = nil
			},
			peer:   func() *PortState { return nil },
			want:   CauseOpticMissing,
			wantIn: "optic 为空",
		},
		{
			name: "模块不在位",
			mutate: func(p *PortState) {
				p.Optic.Present = false
				p.Optic.Model = ""
			},
			peer:   func() *PortState { return nil },
			want:   CauseOpticMissing,
			wantIn: "不在位",
		},
		{
			name: "模块在位但型号为空",
			mutate: func(p *PortState) {
				p.Optic.Model = "   "
			},
			peer:   func() *PortState { return nil },
			want:   CauseOpticMissing,
			wantIn: "未识别",
		},
		{
			name: "DDM 收光读不到",
			mutate: func(p *PortState) {
				p.Optic.RxPowerDbm = nil
			},
			peer:   func() *PortState { return nil },
			want:   CauseOpticPower,
			wantIn: "读数缺失",
		},
		{
			name: "收光低于门限",
			mutate: func(p *PortState) {
				p.Optic.RxPowerDbm = ptrFloat(-15.8)
			},
			peer:   func() *PortState { return nil },
			want:   CauseOpticPower,
			wantIn: "-15.8 dBm",
		},
		{
			name: "收光正好等于门限也算越限",
			mutate: func(p *PortState) {
				p.Optic.RxPowerDbm = ptrFloat(-14.0)
			},
			peer:   func() *PortState { return nil },
			want:   CauseOpticPower,
			wantIn: "-14.0 dBm",
		},
		{
			name: "速率不符且符号错包到阈值，判协商失败",
			mutate: func(p *PortState) {
				p.IfSpeedMbps = 100
				p.ExpectMbps = 1000
				p.SymbolErrors = 1500
				p.SymbolErrorsPrev = 1000
			},
			peer:   goodPeer,
			want:   CauseNegotiateFail,
			wantIn: "+500",
		},
		{
			name: "速率不符但符号错包没涨，先别判协商",
			mutate: func(p *PortState) {
				p.IfSpeedMbps = 0
				p.ExpectMbps = 10000
				p.SymbolErrors = 1499
				p.SymbolErrorsPrev = 1000
			},
			peer:   goodPeer,
			want:   CauseOneWay,
			wantIn: "本端收不到对端的光",
		},
		{
			name: "符号错包计数器回退，增量按 0 算",
			mutate: func(p *PortState) {
				p.IfSpeedMbps = 100
				p.ExpectMbps = 1000
				p.SymbolErrors = 5
				p.SymbolErrorsPrev = 90000
			},
			peer:   func() *PortState { return nil },
			want:   CauseUnknownDown,
			wantIn: "对端未纳入快照",
		},
		{
			name:   "本端 down 对端 up，本端收不到光",
			mutate: func(*PortState) {},
			peer:   goodPeer,
			want:   CauseOneWay,
			wantIn: "本端收不到对端的光",
		},
		{
			name: "本端 up 对端 down，对端收不到光",
			mutate: func(p *PortState) {
				p.IfOperStatus = operUp
			},
			peer: func() *PortState {
				q := goodPort()
				return &q
			},
			want:   CauseOneWay,
			wantIn: "对端收不到本端的光",
		},
		{
			name: "两端都 up",
			mutate: func(p *PortState) {
				p.IfOperStatus = operUp
			},
			peer:   goodPeer,
			want:   CauseLinkOK,
			wantIn: "均 up",
		},
		{
			name: "两端都 up 但收光差越限，落单向链路",
			mutate: func(p *PortState) {
				p.IfOperStatus = operUp
				p.Optic.RxPowerDbm = ptrFloat(-1.8)
			},
			peer: func() *PortState {
				q := goodPort()
				q.IfOperStatus = operUp
				q.Optic.RxPowerDbm = ptrFloat(-15.4)
				return &q
			},
			want:   CauseOneWay,
			wantIn: "收光差 13.6 dB",
		},
		{
			name: "收光差正好等于门限不算越限",
			mutate: func(p *PortState) {
				p.IfOperStatus = operUp
				p.Optic.RxPowerDbm = ptrFloat(-4.0)
			},
			peer: func() *PortState {
				q := goodPort()
				q.IfOperStatus = operUp
				q.Optic.RxPowerDbm = ptrFloat(-14.0)
				return &q
			},
			want:   CauseLinkOK,
			wantIn: "均 up",
		},
		{
			name: "本端 up 但对端没采到，单向验不了",
			mutate: func(p *PortState) {
				p.IfOperStatus = operUp
			},
			peer:   func() *PortState { return nil },
			want:   CauseLinkOK,
			wantIn: "单向无法验证",
		},
		{
			name:   "两端都 down、设备侧没异常，交给现场",
			mutate: func(*PortState) {},
			peer: func() *PortState {
				q := goodPort()
				return &q
			},
			want:   CauseUnknownDown,
			wantIn: "需现场查",
		},
		{
			name: "lowerLayerDown 的状态名要带进判据",
			mutate: func(p *PortState) {
				p.IfOperStatus = operLowerLayerDown
			},
			peer: func() *PortState {
				q := goodPort()
				return &q
			},
			want:   CauseUnknownDown,
			wantIn: "lowerLayerDown",
		},
	}

	for _, c := range cases {
		p := goodPort()
		c.mutate(&p)
		gotCause, gotEvidence := decidePort(p, c.peer())
		if gotCause != c.want {
			t.Errorf("%s: 成因 = %q, 期望 %q（判据: %s）", c.name, gotCause, c.want, gotEvidence)
			continue
		}
		if !strings.Contains(gotEvidence, c.wantIn) {
			t.Errorf("%s: 判据里应当包含 %q，实际 %q", c.name, c.wantIn, gotEvidence)
		}
	}
}

func TestCauseMetadataComplete(t *testing.T) {
	all := []string{
		CauseSelfAdminDown, CausePeerAdminDown, CauseOpticMissing,
		CauseOpticPower, CauseNegotiateFail, CauseOneWay,
		CauseLinkOK, CauseUnknownDown,
	}
	for _, c := range all {
		if causeZH[c] == "" {
			t.Errorf("成因 %q 缺中文名", c)
		}
		if _, ok := causeOwner[c]; !ok {
			t.Errorf("成因 %q 缺归属映射", c)
		}
		if _, ok := causeNext[c]; !ok {
			t.Errorf("成因 %q 缺下一步动作", c)
		}
	}
	// 六个真成因都要有归属，不能落在空串上。
	six := all[:6]
	for _, c := range six {
		if causeOwner[c] == "" {
			t.Errorf("成因 %q 是六成因之一，必须有归属", c)
		}
	}
	if causeOwner[CauseLinkOK] != "" {
		t.Errorf("端口无异常时不该有归属，实际 %q", causeOwner[CauseLinkOK])
	}
	if causeNext[CauseLinkOK] != "" {
		t.Errorf("端口无异常时不该给下一步动作，实际 %q", causeNext[CauseLinkOK])
	}
}

func TestLinkPeers(t *testing.T) {
	a := goodPort()
	a.Device, a.IfName, a.LinkID = "sw-a", "Ethernet1/1", "L1"
	b := goodPort()
	b.Device, b.IfName, b.LinkID = "sw-b", "Ethernet1/1", "L1"
	lonely := goodPort()
	lonely.Device, lonely.IfName = "sw-c", "Ethernet1/1"

	peers, err := linkPeers([]PortState{a, b, lonely})
	if err != nil {
		t.Fatal(err)
	}
	if got := peers[a.key()]; got == nil || got.Device != "sw-b" {
		t.Errorf("sw-a 的对端应当是 sw-b，实际 %+v", got)
	}
	if got := peers[b.key()]; got == nil || got.Device != "sw-a" {
		t.Errorf("配对必须双向，sw-b 的对端实际 %+v", got)
	}
	if _, ok := peers[lonely.key()]; ok {
		t.Error("没登记 link_id 的端口不该有对端")
	}
}

func TestLinkPeersRejectsBentLedger(t *testing.T) {
	a := goodPort()
	a.Device, a.IfName, a.LinkID = "sw-a", "Ethernet1/1", "L1"
	b := goodPort()
	b.Device, b.IfName, b.LinkID = "sw-b", "Ethernet1/1", "L1"
	c := goodPort()
	c.Device, c.IfName, c.LinkID = "sw-c", "Ethernet1/1", "L1"

	if _, err := linkPeers([]PortState{a, b, c}); err == nil {
		t.Error("一条链路挂三个端口应当报错，不能猜哪个是对端")
	}
	if _, err := linkPeers([]PortState{a, a}); err == nil {
		t.Error("重复端口应当报错")
	}

	bad := goodPort()
	bad.IfAdminStatus = 3
	if _, err := linkPeers([]PortState{bad}); err == nil {
		t.Error("ifAdminStatus 越界应当报错")
	}
}

func TestRxImbalance(t *testing.T) {
	portWithRx := func(device string, rx float64) PortState {
		p := goodPort()
		p.Device = device
		p.IfOperStatus = operUp
		p.Optic.RxPowerDbm = ptrFloat(rx)
		return p
	}

	type rxCase struct {
		name   string
		here   PortState
		there  PortState
		wantIn []string
	}

	cases := []rxCase{
		{
			name:   "差 13.6 dB，低的那端在对端",
			here:   portWithRx("sw-a", -1.8),
			there:  portWithRx("sw-b", -15.4),
			wantIn: []string{"13.6 dB", "超过 10 dB 门限", "低的那端是 sw-b Ethernet1/1"},
		},
		{
			name:   "调用方向反了，低的那端也要指对",
			here:   portWithRx("sw-a", -15.4),
			there:  portWithRx("sw-b", -1.8),
			wantIn: []string{"13.6 dB", "低的那端是 sw-a Ethernet1/1"},
		},
		{
			name:  "差正好 10 dB 不越限",
			here:  portWithRx("sw-a", -4.0),
			there: portWithRx("sw-b", -14.0),
		},
		{
			name:  "差 9.9 dB 不越限",
			here:  portWithRx("sw-a", -4.0),
			there: portWithRx("sw-b", -13.9),
		},
		{
			name:  "两端收光一样，没话说",
			here:  portWithRx("sw-a", -6.0),
			there: portWithRx("sw-b", -6.0),
		},
	}

	// 收光读不到时这个判据直接失效，不能当成「差值为 0」也不能当「越限」。
	noRx := portWithRx("sw-a", -1.8)
	noRx.Optic.RxPowerDbm = nil
	peerNoRx := portWithRx("sw-b", -15.4)
	peerNoRx.Optic.RxPowerDbm = nil
	peerNoOptic := portWithRx("sw-b", -15.4)
	peerNoOptic.Optic = nil
	cases = append(cases,
		rxCase{"本端收光读不到", portWithRx("sw-a", 0), noRx, nil},
		rxCase{"对端收光读不到", portWithRx("sw-a", -1.8), peerNoRx, nil},
		rxCase{"对端没有光模块记录", portWithRx("sw-a", -1.8), peerNoOptic, nil},
	)

	for _, c := range cases {
		got := rxImbalance(c.here, &c.there)
		if len(c.wantIn) == 0 {
			if got != "" {
				t.Errorf("%s: 不该判失衡，实际 %q", c.name, got)
			}
			continue
		}
		for _, want := range c.wantIn {
			if !strings.Contains(got, want) {
				t.Errorf("%s: 判据里应当包含 %q，实际 %q", c.name, want, got)
			}
		}
	}
}

func TestMaxInWindow(t *testing.T) {
	cases := []struct {
		name   string
		events []int64
		window int64
		want   int
	}{
		{"空输入", nil, 600_000, 0},
		{"全在一个窗口", []int64{0, 1000, 2000}, 600_000, 3},
		{"分成两段", []int64{0, 1000, 2000, 700_000, 700_500}, 600_000, 3},
		{"窗口边界正好落在跨度上", []int64{0, 600_000}, 600_000, 2},
		{"超出边界一秒", []int64{0, 600_001}, 600_000, 1},
	}
	for _, c := range cases {
		if got := maxInWindow(c.events, c.window); got != c.want {
			t.Errorf("%s: maxInWindow = %d, 期望 %d", c.name, got, c.want)
		}
	}
}

func newTrap(ts, device string, ifIndex int, kind string, uptimeMs int64) Trap {
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	return Trap{
		TS: ts, Device: device, IfIndex: ifIndex, Kind: kind,
		SysUpTimeMs: uptimeMs, unixMs: parsed.UnixMilli(),
	}
}

func TestCountFlipsWindow(t *testing.T) {
	idx := map[indexKey]string{{device: "sw-a", ifIndex: 9}: "Ethernet1/9"}
	traps := []Trap{
		newTrap("2026-05-12T10:00:05Z", "sw-a", 9, trapLinkDown, 3_600_000),
		newTrap("2026-05-12T10:00:48Z", "sw-a", 9, trapLinkUp, 3_643_000),
		newTrap("2026-05-12T10:02:40Z", "sw-a", 9, trapLinkDown, 3_755_000),
		// 隔了 20 分钟的老事件，不该进同一个窗口
		newTrap("2026-05-12T10:22:40Z", "sw-a", 9, trapLinkUp, 3_875_000),
	}
	flips, reboots, dropped := countFlips(traps, idx, defaultFlapPolicy)
	if len(reboots) != 0 {
		t.Errorf("sysUpTime 单调，不该识别出重启: %+v", reboots)
	}
	if dropped != 0 {
		t.Errorf("没有冷启动期的 Trap，不该有剔除，实际 %d", dropped)
	}
	if got := flips[portKey{device: "sw-a", ifName: "Ethernet1/9"}]; got != 3 {
		t.Errorf("窗口内应当是 3 次翻转，实际 %d", got)
	}
}

func TestCountFlipsDropsAgentColdStart(t *testing.T) {
	idx := map[indexKey]string{{device: "sw-a", ifIndex: 9}: "Ethernet1/9"}
	traps := []Trap{
		newTrap("2026-05-12T10:00:47Z", "sw-a", 9, trapLinkUp, 4_030_000),
		// 设备重启，sysUpTime 归零
		newTrap("2026-05-12T10:03:05Z", "sw-a", 9, trapLinkDown, 12_000),
		newTrap("2026-05-12T10:03:11Z", "sw-a", 9, trapLinkUp, 18_000),
		newTrap("2026-05-12T10:03:18Z", "sw-a", 9, trapLinkDown, 25_000),
		newTrap("2026-05-12T10:03:26Z", "sw-a", 9, trapLinkUp, 33_000),
	}
	flips, reboots, dropped := countFlips(traps, idx, defaultFlapPolicy)

	if len(reboots) != 1 {
		t.Fatalf("应当识别出 1 次重启，实际 %d: %+v", len(reboots), reboots)
	}
	if reboots[0].Device != "sw-a" || reboots[0].UptimeMs != 12_000 {
		t.Errorf("重启记录不对: %+v", reboots[0])
	}
	if dropped != 4 {
		t.Errorf("冷启动期应当剔除 4 条，实际 %d", dropped)
	}
	if reboots[0].Dropped != 4 {
		t.Errorf("重启记录里也该记 4 条剔除，实际 %d", reboots[0].Dropped)
	}
	k := portKey{device: "sw-a", ifName: "Ethernet1/9"}
	if got := flips[k]; got != 1 {
		t.Errorf("剔除冷启动期后只该剩 1 次翻转，实际 %d —— 这就是假抖动", got)
	}
}

func TestCountFlipsIgnoresUnmappedIfIndex(t *testing.T) {
	idx := map[indexKey]string{{device: "sw-a", ifIndex: 9}: "Ethernet1/9"}
	traps := []Trap{
		newTrap("2026-05-12T10:00:05Z", "sw-a", 77, trapLinkDown, 3_600_000),
	}
	flips, _, _ := countFlips(traps, idx, defaultFlapPolicy)
	if len(flips) != 0 {
		t.Errorf("映射不上的 ifIndex 不该记到任何端口上，实际 %+v", flips)
	}
}

func TestDecideOnSamples(t *testing.T) {
	ports, err := loadPorts("samples/portstate.json")
	if err != nil {
		t.Fatal(err)
	}
	traps, err := loadTraps("samples/traps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := Decide(ports, traps, defaultFlapPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Verdicts) != len(ports) {
		t.Fatalf("每个端口都该有一条判定，%d != %d", len(analysis.Verdicts), len(ports))
	}

	want := map[portKey]struct {
		cause    string
		owner    string
		flips    int
		flapping bool
	}{
		{"tor-a3f-01", "Ethernet1/1"}:  {CauseSelfAdminDown, OwnerChange, 1, false},
		{"tor-a3f-01", "Ethernet1/2"}:  {CausePeerAdminDown, OwnerChange, 0, false},
		{"tor-a3f-01", "Ethernet1/3"}:  {CauseOpticMissing, OwnerHardware, 0, false},
		{"tor-a3f-01", "Ethernet1/4"}:  {CauseOpticMissing, OwnerHardware, 0, false},
		{"tor-a3f-01", "Ethernet1/9"}:  {CauseLinkOK, "", 6, true},
		{"tor-a3f-01", "Ethernet1/48"}: {CauseOpticPower, OwnerHardware, 1, false},
		{"tor-a3f-02", "Ethernet1/2"}:  {CauseSelfAdminDown, OwnerChange, 1, false},
		{"tor-a3f-02", "Ethernet1/5"}:  {CauseOpticPower, OwnerHardware, 1, false},
		{"tor-a3f-02", "Ethernet1/6"}:  {CauseOpticPower, OwnerHardware, 1, false},
		{"tor-a3f-02", "Ethernet1/7"}:  {CauseNegotiateFail, OwnerConfig, 1, false},
		{"tor-a3f-02", "Ethernet1/9"}:  {CauseLinkOK, "", 1, false},
		{"tor-a3f-02", "Ethernet1/46"}: {CauseUnknownDown, OwnerFault, 0, false},
		{"tor-a3f-02", "Ethernet1/48"}: {CauseLinkOK, "", 0, false},
		{"spine-01", "Ethernet1/1"}:    {CauseOneWay, OwnerFault, 0, false},
		{"spine-01", "Ethernet1/2"}:    {CauseLinkOK, "", 0, false},
		{"spine-01", "Ethernet1/9"}:    {CauseLinkOK, "", 0, false},
		{"spine-01", "Ethernet1/10"}:   {CauseOneWay, OwnerFault, 0, false},
		{"spine-02", "Ethernet1/9"}:    {CauseLinkOK, "", 0, false},
		{"spine-02", "Ethernet1/10"}:   {CauseOneWay, OwnerFault, 0, false},
		{"spine-02", "Ethernet1/11"}:   {CauseUnknownDown, OwnerFault, 0, false},
	}
	for _, v := range analysis.Verdicts {
		k := portKey{device: v.Device, ifName: v.IfName}
		w, ok := want[k]
		if !ok {
			t.Errorf("样本里多出一个没登记的端口 %s %s", v.Device, v.IfName)
			continue
		}
		if v.Cause != w.cause {
			t.Errorf("%s %s: 成因 = %q, 期望 %q（判据: %s）", v.Device, v.IfName, v.Cause, w.cause, v.Evidence)
		}
		if v.Owner != w.owner {
			t.Errorf("%s %s: 归属 = %q, 期望 %q", v.Device, v.IfName, v.Owner, w.owner)
		}
		if v.FlapCount != w.flips || v.Flapping != w.flapping {
			t.Errorf("%s %s: 抖动 = %d/%v, 期望 %d/%v",
				v.Device, v.IfName, v.FlapCount, v.Flapping, w.flips, w.flapping)
		}
		if v.CauseZH == "" {
			t.Errorf("%s %s: 缺中文成因", v.Device, v.IfName)
		}
	}
	if len(want) != len(analysis.Verdicts) {
		t.Errorf("期望表漏了端口：期望 %d 条，实际 %d 条", len(want), len(analysis.Verdicts))
	}

	if len(analysis.Reboots) != 1 {
		t.Fatalf("样本里应当识别出 1 次重启，实际 %d", len(analysis.Reboots))
	}
	if analysis.Reboots[0].Device != "tor-a3f-02" || analysis.Reboots[0].Dropped != 6 {
		t.Errorf("重启记录不对: %+v", analysis.Reboots[0])
	}
	if analysis.DroppedTraps != 6 {
		t.Errorf("样本应当剔除 6 条冷启动期 Trap，实际 %d", analysis.DroppedTraps)
	}
}

// TestFlapPolicyIsPolicy 说明 M 和 N 是策略：阈值放到 1，同一个端口就从
// 「不算抖动」变成「算抖动」。口径不清，两边就能拿同一份数据吵起来。
func TestFlapPolicyIsPolicy(t *testing.T) {
	ports, err := loadPorts("samples/portstate.json")
	if err != nil {
		t.Fatal(err)
	}
	traps, err := loadTraps("samples/traps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := Decide(ports, traps, FlapPolicy{WindowMs: 600_000, MinFlips: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range analysis.Verdicts {
		if v.Device == "tor-a3f-02" && v.IfName == "Ethernet1/9" {
			if !v.Flapping {
				t.Errorf("阈值降到 1 后，1 次翻转也该算 flapping")
			}
			return
		}
	}
	t.Fatal("样本里找不到 tor-a3f-02 Ethernet1/9")
}
