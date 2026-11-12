package main

import "testing"

func TestParseOID(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"1.3.6.1.2.1.2.2.1.2", "1.3.6.1.2.1.2.2.1.2", false},
		{".1.3.6", "1.3.6", false},
		{"  1.3.6  ", "1.3.6", false},
		{"", "", true},
		{"1..3", "", true},
		{"1.3.x", "", true},
		{"1.3.-4", "", true},
	}
	for _, c := range cases {
		got, err := ParseOID(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("ParseOID(%q) 期望报错，实际得到 %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseOID(%q) 报错: %v", c.in, err)
		}
		if got.String() != c.want {
			t.Fatalf("ParseOID(%q) = %s，期望 %s", c.in, got, c.want)
		}
	}
}

func TestOIDCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.3.6", "1.3.6", 0},
		{"1.3.6", "1.3.6.1", -1}, // 前缀短的排前面
		{"1.3.6.1", "1.3.6", 1},
		{"1.3.6.1.2.1.2.2.1.2.1", "1.3.6.1.2.1.2.2.1.2.2", -1},
		{"1.3.6.1.2.1.31.1.1.1.1", "1.3.6.1.2.1.4.20.1.1", 1}, // 31 > 4
	}
	for _, c := range cases {
		if got := MustOID(c.a).Compare(MustOID(c.b)); got != c.want {
			t.Fatalf("%s.Compare(%s) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestOIDHasPrefix(t *testing.T) {
	col := MustOID("1.3.6.1.2.1.2.2.1.2")
	cases := []struct {
		oid  string
		want bool
	}{
		{"1.3.6.1.2.1.2.2.1.2.5", true},
		{"1.3.6.1.2.1.2.2.1.2", true},
		{"1.3.6.1.2.1.2.2.1.3.5", false}, // 隔壁那一列
		{"1.3.6.1.2.1.2.2.1", false},     // 比列还短
		{"1.3.6.1.2.1.4.20.1.1.10.0.0.11", false},
	}
	for _, c := range cases {
		if got := MustOID(c.oid).HasPrefix(col); got != c.want {
			t.Fatalf("%s.HasPrefix(%s) = %v，期望 %v", c.oid, col, got, c.want)
		}
	}
}

func TestOIDChildLast(t *testing.T) {
	col := MustOID("1.3.6.1.2.1.31.1.1.1.1")
	cell := col.Child(7)
	if cell.String() != "1.3.6.1.2.1.31.1.1.1.1.7" {
		t.Fatalf("Child 拼出来的 OID 不对: %s", cell)
	}
	if cell.Last() != 7 {
		t.Fatalf("行索引应为 7，实际 %d", cell.Last())
	}
	// Child 不能改到底层数组：列常量被所有行共享。
	if len(col) != 11 {
		t.Fatalf("Child 改动了调用方的 OID: %v（长度 %d）", col, len(col))
	}
}

func TestOIDClone(t *testing.T) {
	a := MustOID("1.3.6")
	b := a.Clone()
	b[2] = 9
	if a[2] != 6 {
		t.Fatalf("Clone 后仍然共享底层数组: %v", a)
	}
}
