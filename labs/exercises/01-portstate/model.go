package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ifAdminStatus / ifOperStatus 的取值，照 RFC 2863 抄，不另外发明一套。
//
// ifOperStatus 有七个值，多数平台只认前两个。样本里刻意留了 notPresent
// 与 lowerLayerDown 各一个端口——把后五个当成 down 处理，等于扔掉定位线索。
const (
	adminUp   = 1
	adminDown = 2

	operUp             = 1
	operDown           = 2
	operTesting        = 3
	operUnknown        = 4
	operDormant        = 5
	operNotPresent     = 6
	operLowerLayerDown = 7
)

// operName 把 ifOperStatus 的整数翻成人读的名字，认不出的值原样打出来。
func operName(v int) string {
	switch v {
	case operUp:
		return "up"
	case operDown:
		return "down"
	case operTesting:
		return "testing"
	case operUnknown:
		return "unknown"
	case operDormant:
		return "dormant"
	case operNotPresent:
		return "notPresent"
	case operLowerLayerDown:
		return "lowerLayerDown"
	default:
		return fmt.Sprintf("oper(%d)", v)
	}
}

// adminName 只有两个取值，没有第三态。
func adminName(v int) string {
	if v == adminUp {
		return "up"
	}
	return "down"
}

// Optic 是一个端口上的光模块状态。
//
// 收光是判定光功率异常的唯一输入；温度、电压、偏置电流留给第 06 课，
// 这里先不收，免得样本里堆一堆用不上的数。
type Optic struct {
	Present bool `json:"present"`
	// Model 为空表示模块在位但设备没识别出型号。
	Model string `json:"model"`
	// RxPowerDbm 为 null 表示 DDM 读不到收光，不等于收光为 0。
	RxPowerDbm    *float64 `json:"rx_power_dbm"`
	TxPowerDbm    *float64 `json:"tx_power_dbm"`
	RxLowAlarmDbm float64  `json:"rx_low_alarm_dbm"`
}

// PortState 是 ifTable 快照里的一行。字段名贴 SNMP 原义，方便对着 MIB 抄。
//
// 主键用 device + if_name，不用 ifIndex：ifIndex 换块业务板就重排，
// 拿它当主键会把新端口的历史接在旧端口上（第 16 课）。
type PortState struct {
	Device        string `json:"device"`
	IfName        string `json:"if_name"`
	IfAlias       string `json:"if_alias"`
	IfIndex       int    `json:"if_index"`
	IfAdminStatus int    `json:"if_admin_status"`
	IfOperStatus  int    `json:"if_oper_status"`
	IfSpeedMbps   int    `json:"if_speed_mbps"`
	// ExpectMbps 来自台账：这个口按设计该跑多少速率。0 表示台账没登记。
	ExpectMbps int `json:"expect_mbps"`
	// dot3StatsSymbolErrors 的当前值与上一周期值，增量用来判协商失败。
	SymbolErrors     int64  `json:"dot3_symbol_errors"`
	SymbolErrorsPrev int64  `json:"dot3_symbol_errors_prev"`
	LinkID           string `json:"link_id"`
	Optic            *Optic `json:"optic"`
}

// portKey 是端口主键。链路配对、抖动计数都用它。
type portKey struct {
	device string
	ifName string
}

func (p PortState) key() portKey {
	return portKey{device: p.Device, ifName: p.IfName}
}

func (p PortState) operUp() bool {
	return p.IfOperStatus == operUp
}

// Trap 是录下来的一个 SNMP Trap。TS 用 RFC3339 写，样本要能手读。
//
// 真实 Trap 里只有 ifIndex，没有端口名——要还原成 ifName 得靠快照里的
// ifIndex → ifName 映射。这一步做错，抖动计数就全记到别人头上。
type Trap struct {
	TS          string `json:"ts"`
	Device      string `json:"device"`
	IfIndex     int    `json:"if_index"`
	Kind        string `json:"kind"`
	SysUpTimeMs int64  `json:"sys_uptime_ms"`

	// unixMs 由 loadTraps 从 TS 解析出来，样本里不写。
	unixMs int64
}

const (
	trapLinkDown = "link_down"
	trapLinkUp   = "link_up"
)

// loadPorts 读 ifTable 快照（JSON 数组）。
func loadPorts(path string) ([]PortState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ports []PortState
	if err := json.Unmarshal(data, &ports); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("%s: 快照是空的", path)
	}
	return ports, nil
}

// loadTraps 读 Trap 序列（JSONL，一行一条），顺手把时间戳解成毫秒。
func loadTraps(path string) ([]Trap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var traps []Trap
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var t Trap
		if err := json.Unmarshal([]byte(text), &t); err != nil {
			return nil, fmt.Errorf("%s 第 %d 行: %w", path, line, err)
		}
		ts, err := time.Parse(time.RFC3339, t.TS)
		if err != nil {
			return nil, fmt.Errorf("%s 第 %d 行: ts 不是 RFC3339: %w", path, line, err)
		}
		if t.Kind != trapLinkDown && t.Kind != trapLinkUp {
			return nil, fmt.Errorf("%s 第 %d 行: 不认识的 kind %q", path, line, t.Kind)
		}
		t.unixMs = ts.UnixMilli()
		traps = append(traps, t)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return traps, nil
}
