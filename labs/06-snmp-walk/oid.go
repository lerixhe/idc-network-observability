package main

import (
	"fmt"
	"strconv"
	"strings"
)

// OID 是 SNMP 的树形命名空间里的一条路径。
//
// 一台设备上所有可取值的东西排成一棵字典序的树：每一级是一个非负整数，
// 比较两个 OID 就是逐级比整数。GetBulk 之所以能「往下取 N 条」，
// 靠的就是这个全序——它不需要知道表结构，只会「给我比这个大一点的 N 条」。
type OID []uint32

// ParseOID 解析点分十进制，如 "1.3.6.1.2.1.2.2.1.2"。
func ParseOID(s string) (OID, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), ".")
	if s == "" {
		return nil, fmt.Errorf("OID 不能为空")
	}
	parts := strings.Split(s, ".")
	out := make(OID, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("OID 段 %q 不是无符号整数", p)
		}
		out = append(out, uint32(n))
	}
	return out, nil
}

// MustOID 只给代码里写死的常量用；样本文件里的 OID 一律走 ParseOID 并回报错误。
func MustOID(s string) OID {
	o, err := ParseOID(s)
	if err != nil {
		panic(err)
	}
	return o
}

func (o OID) String() string {
	parts := make([]string, len(o))
	for i, v := range o {
		parts[i] = strconv.FormatUint(uint64(v), 10)
	}
	return strings.Join(parts, ".")
}

// Compare 返回 -1 / 0 / 1。前缀短的排前面：1.3.6 小于 1.3.6.1。
func (o OID) Compare(b OID) int {
	n := len(o)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		switch {
		case o[i] < b[i]:
			return -1
		case o[i] > b[i]:
			return 1
		}
	}
	switch {
	case len(o) < len(b):
		return -1
	case len(o) > len(b):
		return 1
	}
	return 0
}

// HasPrefix 判断 o 是否落在 p 这棵子树里。表遍历的停止条件就这一句：
// 返回的 OID 一旦不再以列的 OID 开头，说明这张表已经走到头了。
func (o OID) HasPrefix(p OID) bool {
	if len(o) < len(p) {
		return false
	}
	for i := range p {
		if o[i] != p[i] {
			return false
		}
	}
	return true
}

// Child 拼一级子节点，列 OID + 行索引就是单元格 OID。
func (o OID) Child(sub uint32) OID {
	out := make(OID, len(o), len(o)+1)
	copy(out, o)
	return append(out, sub)
}

// Last 取最后一级。对 1.3.6.1.2.1.31.1.1.1.1.5 来说就是行索引 5。
func (o OID) Last() uint32 {
	if len(o) == 0 {
		return 0
	}
	return o[len(o)-1]
}

// Clone 复制一份，免得切片共享底层数组被后续 append 改掉。
func (o OID) Clone() OID {
	out := make(OID, len(o))
	copy(out, o)
	return out
}
