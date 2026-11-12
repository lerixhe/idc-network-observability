package main

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Transport 是一次请求的传输层。样本模式与 UDP 模式都实现它，
// 采集器（collector.go）看不到差别。
//
// 换掉这一层就能接真实设备，前提是补一个 BER 编解码——本实验没做，
// 见 README「为什么 wire 层是 JSON」。
type Transport interface {
	Request(dev string, req BulkRequest, timeout time.Duration) (Response, time.Duration)
	// Kind 说明这一层会不会自己消耗真实时间：样本模式不会，UDP 会。
	Kind() string
}

// SampleTransport 把请求直接交给内存里的 agent，时延由样本里的
// response_ms / jitter_ms 决定，超时也按这张表折算。
type SampleTransport struct {
	agents map[string]*Agent
}

// NewSampleTransport 给样本里每台设备配一个 agent。
// 种子按设备名派生，同样的样本 + 同样的种子必定同样的抖动。
func NewSampleTransport(f *Fleet, seed int64) *SampleTransport {
	t := &SampleTransport{agents: make(map[string]*Agent, len(f.Devices))}
	for i := range f.Devices {
		t.agents[f.Devices[i].Name] = NewAgent(&f.Devices[i], seed+int64(i)*7919)
	}
	return t
}

func (t *SampleTransport) Kind() string { return "sample" }

func (t *SampleTransport) Request(dev string, req BulkRequest, timeout time.Duration) (Response, time.Duration) {
	a, ok := t.agents[dev]
	if !ok {
		return Response{Error: ErrTimeout}, timeout
	}
	resp := a.Simulate(req, timeout)
	return resp, time.Duration(resp.ElapsedMs) * time.Millisecond
}

// UDPServer 把内置 agent 挂到 127.0.0.1 的一个端口上，报文是 JSON。
//
// 这一层能验证真实套接字上的超时、丢包、往返耗时；报文格式不是 SNMP，
// 所以它验证不了 BER 的长度编码陷阱——那部分本实验明确不做。
type UDPServer struct {
	conn   *net.UDPConn
	agents map[string]*Agent
}

// ServeUDP 起一个回环服务。addr 用 "127.0.0.1:0" 让内核分配端口。
func ServeUDP(addr string, f *Fleet, seed int64) (*UDPServer, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("解析监听地址 %s: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, fmt.Errorf("监听 %s: %w", addr, err)
	}
	s := &UDPServer{conn: conn, agents: make(map[string]*Agent, len(f.Devices))}
	for i := range f.Devices {
		s.agents[f.Devices[i].Name] = NewAgent(&f.Devices[i], seed+int64(i)*7919)
	}
	go s.loop()
	return s, nil
}

// Addr 返回真实监听地址（端口可能是内核分配的）。
func (s *UDPServer) Addr() string { return s.conn.LocalAddr().String() }

func (s *UDPServer) Close() error { return s.conn.Close() }

func (s *UDPServer) loop() {
	buf := make([]byte, 64<<10)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return // 连接关了
		}
		raw := make([]byte, n)
		copy(raw, buf[:n])
		go s.handle(raw, from)
	}
}

func (s *UDPServer) handle(raw []byte, from *net.UDPAddr) {
	var req BulkRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return
	}
	a, ok := s.agents[req.Device]
	if !ok || a.Profile.Unreachable {
		// 不可达的设备就是不回包。回一个「我不在」比不回更假。
		return
	}
	if req.MaxRepetitions > a.Profile.MaxRepetitions {
		s.reply(from, req.Device, Response{Error: ErrTooBig})
		return
	}
	// 真实设备的管理面时延，这里真的等。
	delay := time.Duration(a.Profile.ResponseMs)*time.Millisecond + a.jitter()
	if delay > 0 {
		time.Sleep(delay)
	}
	s.reply(from, req.Device, Response{Varbinds: a.GetBulk(req.Start, req.MaxRepetitions)})
}

func (s *UDPServer) reply(to *net.UDPAddr, dev string, resp Response) {
	payload, err := json.Marshal(wireResponse{Device: dev, Response: resp})
	if err != nil {
		return
	}
	_, _ = s.conn.WriteToUDP(payload, to)
}

// UDPTransport 是真套接字客户端：每次请求一个 deadline，超时就是超时。
type UDPTransport struct {
	conn *net.UDPConn
}

// DialUDP 连到本地 agent。
func DialUDP(addr string) (*UDPTransport, error) {
	ra, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("解析 agent 地址 %s: %w", addr, err)
	}
	conn, err := net.DialUDP("udp", nil, ra)
	if err != nil {
		return nil, fmt.Errorf("连接 agent %s: %w", addr, err)
	}
	return &UDPTransport{conn: conn}, nil
}

func (t *UDPTransport) Kind() string { return "udp" }

func (t *UDPTransport) Close() error { return t.conn.Close() }

func (t *UDPTransport) Request(dev string, req BulkRequest, timeout time.Duration) (Response, time.Duration) {
	req.Device = dev
	payload, err := json.Marshal(req)
	if err != nil {
		return Response{Error: ErrTimeout}, 0
	}
	begin := time.Now()
	if err := t.conn.SetDeadline(begin.Add(timeout)); err != nil {
		return Response{Error: ErrTimeout}, 0
	}
	if _, err := t.conn.Write(payload); err != nil {
		return Response{Error: ErrTimeout}, time.Since(begin)
	}
	buf := make([]byte, 256<<10)
	n, err := t.conn.Read(buf)
	elapsed := time.Since(begin)
	if err != nil {
		return Response{Error: ErrTimeout}, elapsed
	}
	var wr wireResponse
	if err := json.Unmarshal(buf[:n], &wr); err != nil {
		return Response{Error: ErrTimeout}, elapsed
	}
	if wr.Device != dev {
		// 回包不是这台设备的：串包在轮询里只可能是前一次超时的应答迟到了。
		return Response{Error: ErrTimeout}, elapsed
	}
	return wr.Response, elapsed
}

// wireResponse 是回包信封。设备名用来对齐迟到的应答，免得把上一台的
// 回包当成这一台的——UDP 无连接，这一步只能自己做。
type wireResponse struct {
	Device string `json:"device"`
	Response
}
