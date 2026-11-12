package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// 生命周期三态。台账里出现别的值就当脏数据报错，不猜。
//
// retired 不是删：历史指标、历史告警、历史工单里都留着这个对象的名字，
// 删掉记录等于制造一批指向空气的外键（第 23 课那个判据）。
const (
	LifecycleActive  = "active"
	LifecycleStaging = "staging"
	LifecycleRetired = "retired"
)

// Device 是台账里的一条设备记录。
//
// 物理上独一无二的是 serial，不是 hostname。改名之后旧名还留在台账里，
// 拿 hostname 当合并键会把同一台机器算成两台——这个实验的核心差异之一
// 就是从这儿长出来的。
type Device struct {
	Hostname  string `json:"hostname"`
	MgmtIP    string `json:"mgmt_ip"`
	MgmtMAC   string `json:"mgmt_mac"`
	Serial    string `json:"serial"`
	Model     string `json:"model"`
	Site      string `json:"site"`
	Rack      string `json:"rack"`
	Lifecycle string `json:"lifecycle"`
	Owner     string `json:"owner"`
	// AliasOf 非空表示这条记录是另一条记录的旧名。正常台账里为空，
	// 只有 apply 过补丁的副本才会带上它。
	AliasOf string `json:"alias_of,omitempty"`
}

// PortDecl 是台账里的一条端口声明：哪台设备把自己的哪个口，登记成连到谁。
//
// 台账是各设备自报的端口表，不是全局链路表。一根线两端各写一条，
// 两条都写对才算自洽；写岔了，台账本身没有任何裁决能力——这就是
// 「链路是对象不是属性」的反面教材（第 25 课）。
type PortDecl struct {
	Device     string `json:"device"`
	Port       string `json:"port"`
	IfIndex    int    `json:"if_index"`
	PeerDevice string `json:"peer_device"`
	PeerPort   string `json:"peer_port"`
	DeclaredAt string `json:"declared_at"`
}

// Ledger 是台账快照。
type Ledger struct {
	GeneratedAt string     `json:"generated_at"`
	Devices     []Device   `json:"devices"`
	Links       []PortDecl `json:"links"`
}

// LldpSnapshot 是一次 LLDP 采集的结果。
//
// 每台开了 LLDP 的设备，把自己每个端口的本端信息（端口名、ifIndex、
// 管理地址）连同它看到的邻居一起报上来。ifIndex 取的是 LLDP 本地端口号，
// 多数实现直接就是 ifIndex——换块业务板，这个数就全变了。
type LldpSnapshot struct {
	CollectedAt string     `json:"collected_at"`
	Ports       []LldpPort `json:"ports"`
}

// LldpPort 是一个本端端口的上报。
type LldpPort struct {
	Device  string `json:"device"`
	Port    string `json:"port"`
	IfIndex int    `json:"if_index"`
	// MgmtIP 来自 management address TLV。设备没开这个 TLV 就是空串。
	MgmtIP    string         `json:"mgmt_ip"`
	Neighbors []LldpNeighbor `json:"neighbors"`
}

// LldpNeighbor 是端口上看到的一台邻居。
type LldpNeighbor struct {
	Device string `json:"device"`
	Port   string `json:"port"`
	MgmtIP string `json:"mgmt_ip"`
}

// ArpSnapshot 是网关 ARP 表快照：管理网的 IP → MAC。
//
// 用途单一但关键：证明「这个管理地址此刻还有人在用」，以及证明
// 「这个地址上的人已经换了一个」——后者比前者有用。
type ArpSnapshot struct {
	CollectedAt string     `json:"collected_at"`
	Gateway     string     `json:"gateway"`
	Entries     []ArpEntry `json:"entries"`
}

// ArpEntry 是 ARP 表的一行。
type ArpEntry struct {
	IP   string `json:"ip"`
	MAC  string `json:"mac"`
	VLAN int    `json:"vlan"`
}

// devPort 是端口主键。凡是和端口打交道的地方都用它，
// 一次都不用 ifIndex——那是设备本地索引，换块板就重排。
type devPort struct {
	device string
	port   string
}

func (k devPort) String() string { return k.device + "#" + k.port }

// splitTarget 是 devPort.String 的逆运算，补丁里的 target 靠它还原。
func splitTarget(s string) (devPort, error) {
	parts := strings.SplitN(s, "#", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return devPort{}, fmt.Errorf("target %q 不是 device#port 形式", s)
	}
	return devPort{device: parts[0], port: parts[1]}, nil
}

// loadLedger 读台账并做结构校验。
func loadLedger(path string) (*Ledger, error) {
	var l Ledger
	if err := loadJSON(path, &l); err != nil {
		return nil, err
	}
	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &l, nil
}

// loadLLDP 读 LLDP 快照。
func loadLLDP(path string) (*LldpSnapshot, error) {
	var s LldpSnapshot
	if err := loadJSON(path, &s); err != nil {
		return nil, err
	}
	if len(s.Ports) == 0 {
		return nil, fmt.Errorf("%s: 快照里一个端口都没有", path)
	}
	return &s, nil
}

// loadARP 读 ARP 快照。空表是合法的（网关刚清过表），但要把
// 文件的意图和加载失败分开：读不到文件报错，读到空表不报错。
func loadARP(path string) (*ArpSnapshot, error) {
	var s ArpSnapshot
	if err := loadJSON(path, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func loadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Validate 校验台账的结构与枚举值。引用完整性不在这里查：
// 脏台账里「声明指向不存在的设备」是常态，交给对账环节计数处理，
// 在这里拦住只会让人先去修样本。
func (l *Ledger) Validate() error {
	if len(l.Devices) == 0 {
		return errors.New("台账里一台设备都没有")
	}
	seen := make(map[string]bool, len(l.Devices))
	for i, d := range l.Devices {
		if strings.TrimSpace(d.Hostname) == "" {
			return fmt.Errorf("devices[%d] 没有 hostname", i)
		}
		if seen[d.Hostname] {
			return fmt.Errorf("设备 %s 登记了两次（同一个 hostname 两条记录）", d.Hostname)
		}
		seen[d.Hostname] = true
		switch d.Lifecycle {
		case LifecycleActive, LifecycleStaging, LifecycleRetired:
		default:
			return fmt.Errorf("%s 的 lifecycle=%q 不是合法取值（active/staging/retired）",
				d.Hostname, d.Lifecycle)
		}
	}

	seenLink := make(map[devPort]bool, len(l.Links))
	for i, p := range l.Links {
		if p.Device == "" || p.Port == "" {
			return fmt.Errorf("links[%d] 缺 device 或 port", i)
		}
		if p.PeerDevice == "" || p.PeerPort == "" {
			return fmt.Errorf("links[%d]（%s）缺对端", i, devPort{p.Device, p.Port})
		}
		k := devPort{device: p.Device, port: p.Port}
		if seenLink[k] {
			return fmt.Errorf("%s 登记了两条端口声明，一个端口只能有一条", k)
		}
		seenLink[k] = true
	}
	return nil
}
