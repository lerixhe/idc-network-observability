package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ARP 表项多久没被确认就不算数。交换机侧常见 240–1200 秒，
// Linux 内核的 gc_stale_time 是 60 秒。样本按 300 秒算，覆盖大多数网段。
const defaultArpAging = 300 * time.Second

// ArpEntry 是一条 ARP 表项快照。last_seen 不是 ARP 表里的字段，
// 是从表项 age 反推出来的「最后一次被确认的时刻」——没有它就算不出老化了多久。
type ArpEntry struct {
	TS       time.Time `json:"ts"`
	Gateway  string    `json:"gateway"`
	VLAN     int       `json:"vlan"`
	IfName   string    `json:"if_name"`
	IP       string    `json:"ip"`
	MAC      string    `json:"mac"`
	State    string    `json:"state"`
	LastSeen time.Time `json:"last_seen"`
}

// FdbEntry 是一条 MAC 表项。age_s 是表项已经存在的秒数，越小越新。
// 交换机不报「学到时刻」，只报 age，所以跨设备比新旧全靠它。
type FdbEntry struct {
	TS     time.Time `json:"ts"`
	Switch string    `json:"switch"`
	VLAN   int       `json:"vlan"`
	MAC    string    `json:"mac"`
	Port   string    `json:"port"`
	Type   string    `json:"type"`
	AgeS   int       `json:"age_s"`
}

// LeaseRecord 是 DHCP 日志里的一行。日志是事件流，同一个 IP 会有
// ack / release / expire 多行，只有最后一行算数。
type LeaseRecord struct {
	TS       time.Time `json:"ts"`
	Server   string    `json:"server"`
	Event    string    `json:"event"`
	IP       string    `json:"ip"`
	MAC      string    `json:"mac"`
	Hostname string    `json:"hostname"`
	VLAN     int       `json:"vlan"`
	Start    time.Time `json:"start"`
	Expire   time.Time `json:"expire"`
}

// Device 是台账里的设备。这一层只用来核对「这台交换机是不是二层设备」，
// 定位走不到它，留着是为了报错时能说清是哪台设备没采。
type Device struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Rack   string `json:"rack"`
	MgmtIP string `json:"mgmt_ip"`
	Vendor string `json:"vendor"`
	Model  string `json:"model"`
}

// PortRecord 是台账里的端口。members 只有聚合逻辑口才有，
// 逻辑口自己不带机柜 U 位——它没有物理对端。
type PortRecord struct {
	Device  string   `json:"device"`
	Port    string   `json:"port"`
	Rack    string   `json:"rack"`
	U       int      `json:"u"`
	Desc    string   `json:"desc"`
	Members []string `json:"members,omitempty"`
}

// Inventory 是最小台账：设备 + 端口，端口带机柜与 U 位。
type Inventory struct {
	Devices []Device     `json:"devices"`
	Ports   []PortRecord `json:"ports"`

	portIdx map[string]PortRecord
}

// Dataset 是四份样本加载后的全部数据。TS 是查询时刻，
// 取所有快照里最晚的那个——「现在几点」决定了表项算不算新鲜。
type Dataset struct {
	ARP    []ArpEntry
	FDB    []FdbEntry
	Leases []LeaseRecord
	Inv    *Inventory
	TS     time.Time
}

// LoadDataset 读四份样本。任何一份读不动都直接报错退出，
// 少一张表还继续查，答案只会更错。
func LoadDataset(arpPath, fdbPath, leasePath, portPath string) (*Dataset, error) {
	ds := &Dataset{Inv: &Inventory{}}
	if err := readJSONL(arpPath, &ds.ARP); err != nil {
		return nil, fmt.Errorf("读 ARP 快照 %s: %w", arpPath, err)
	}
	if err := readJSONL(fdbPath, &ds.FDB); err != nil {
		return nil, fmt.Errorf("读 FDB 快照 %s: %w", fdbPath, err)
	}
	if err := readJSONL(leasePath, &ds.Leases); err != nil {
		return nil, fmt.Errorf("读 DHCP 租约日志 %s: %w", leasePath, err)
	}
	if err := readJSONFile(portPath, ds.Inv); err != nil {
		return nil, fmt.Errorf("读端口台账 %s: %w", portPath, err)
	}
	ds.Inv.build()
	ds.TS = ds.latestTS()
	return ds, nil
}

// latestTS 取所有快照里最晚的时刻。样本里三源同一时刻；
// 真实环境三源采集时刻不同，这个实验没做时间对齐，README 里点了这一条。
func (d *Dataset) latestTS() time.Time {
	var ts time.Time
	for _, e := range d.ARP {
		if e.TS.After(ts) {
			ts = e.TS
		}
	}
	for _, e := range d.FDB {
		if e.TS.After(ts) {
			ts = e.TS
		}
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return ts
}

func readJSONL[T any](path string, out *[]T) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			return fmt.Errorf("第 %d 行: %w", n, err)
		}
		*out = append(*out, v)
	}
	return sc.Err()
}

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// build 建端口索引。端口名按小写索引：华为报 Ethernet1/7、思科报 Ethernet1/7，
// 但同一家厂商的命令输出里大小写也会飘，索引不统一就会出现「台账明明有」。
func (inv *Inventory) build() {
	inv.portIdx = make(map[string]PortRecord, len(inv.Ports))
	for _, p := range inv.Ports {
		inv.portIdx[portKey(p.Device, p.Port)] = p
	}
}

func portKey(device, port string) string {
	return strings.ToLower(device) + "\x00" + strings.ToLower(port)
}

// Port 按设备名 + 端口名查台账。
func (inv *Inventory) Port(device, port string) (PortRecord, bool) {
	if inv == nil || inv.portIdx == nil {
		return PortRecord{}, false
	}
	rec, ok := inv.portIdx[portKey(device, port)]
	return rec, ok
}

// normalizeMAC 把 MAC 统一成 aa:bb:cc:dd:ee:ff。
// 华为报 0050-56aa-0044，思科报 0050.56aa.0044，Linux 报 00:50:56:aa:00:44——
// 不归一，同一个 MAC 在三张表里就是三个东西，联查必断。
func normalizeMAC(s string) string {
	var hex []byte
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			hex = append(hex, byte(r))
		}
	}
	// 长度不对就原样返回小写，别把异常值吞成空串——否则日志里看不出输入有问题。
	if len(hex) != 12 {
		return strings.ToLower(strings.TrimSpace(s))
	}
	var b strings.Builder
	for i, c := range hex {
		if i > 0 && i%2 == 0 {
			b.WriteByte(':')
		}
		b.WriteByte(c)
	}
	return b.String()
}

// LeaseState 是租约日志加工后的结论。
type LeaseState struct {
	Latest LeaseRecord
	Active bool
	Reason string
}

// leaseState 把日志压成一条结论：最后一行是 ack 且未到期才算有效。
func (d *Dataset) leaseState(ip string, now time.Time) (LeaseState, bool) {
	var best LeaseRecord
	found := false
	for _, l := range d.Leases {
		if l.IP != ip {
			continue
		}
		// 同一秒的重复行按文件顺序取后者，日志本来就是追加写的。
		if !found || !l.TS.Before(best.TS) {
			best, found = l, true
		}
	}
	if !found {
		return LeaseState{}, false
	}

	st := LeaseState{Latest: best}
	switch best.Event {
	case "ack":
		if best.Expire.After(now) {
			st.Active = true
			st.Reason = "在有效期内"
		} else {
			st.Reason = "已过到期时间"
		}
	case "release":
		st.Reason = "客户端已释放"
	case "expire":
		st.Reason = "服务器已回收"
	case "decline":
		st.Reason = "地址冲突被客户端拒绝"
	default:
		st.Reason = "未识别的事件 " + best.Event
	}
	return st, true
}

// inactiveLeases 返回日志里最终没生效的那些 IP 与原因，给 demo 汇总用。
func (d *Dataset) inactiveLeases() []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range d.Leases {
		if seen[l.IP] {
			continue
		}
		seen[l.IP] = true
		st, ok := d.leaseState(l.IP, d.TS)
		if ok && !st.Active {
			out = append(out, fmt.Sprintf("%s %s（%s）", l.IP, st.Latest.Event, st.Reason))
		}
	}
	return out
}

// gatewayCount 是 ARP 快照覆盖到的三层设备数。跨三层查要全采，
// 少一台就有一批 IP 永远定位不到——这个数字要能一眼看出来是 1 还是 2。
func (d *Dataset) gatewayCount() int {
	seen := map[string]bool{}
	for _, e := range d.ARP {
		seen[e.Gateway] = true
	}
	return len(seen)
}

// 聚合口的名字各厂商不同，认这几种。只在台账里查不到这个端口时才用来提示。
var lagPrefixes = []string{
	"port-channel", "portchannel", "po", "eth-trunk",
	"bridge-aggregation", "bagg", "bundle-ether", "ae",
}

func isAggregatePortName(p string) bool {
	l := strings.ToLower(strings.TrimSpace(p))
	for _, pre := range lagPrefixes {
		if !strings.HasPrefix(l, pre) {
			continue
		}
		if _, err := strconv.Atoi(l[len(pre):]); err == nil {
			return true
		}
	}
	return false
}

// durText 把时长压成短形式，证据行里要能一眼比大小：720s 写成 12m，618m 写成 10h18m。
func durText(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	switch {
	case s < 0:
		return "0s"
	case s < 60:
		return strconv.Itoa(s) + "s"
	case s < 3600:
		if s%60 == 0 {
			return strconv.Itoa(s/60) + "m"
		}
		return fmt.Sprintf("%dm%ds", s/60, s%60)
	default:
		if s%3600 == 0 {
			return strconv.Itoa(s/3600) + "h"
		}
		return fmt.Sprintf("%dh%dm", s/3600, s%3600/60)
	}
}
