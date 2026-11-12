package main

import "time"

// 测试里只写数字不写时间单位的地方，用这两个小函数兜住。

func dateTime(y int, mo time.Month, d, h, mi, s, ms int) time.Time {
	return time.Date(y, mo, d, h, mi, s, ms*int(time.Millisecond), time.UTC)
}

func dateDur(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}
