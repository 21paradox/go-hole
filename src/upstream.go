package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

/* ---------- 1. 每个 upstream 的“可重连”连接 ---------- */
type udpUpStream struct {
	pool chan *dns.Conn
	addr string
	// conn *dns.Conn // 长连接
}

/* ---------- 2. 全局 upstream 池 ---------- */
var (
	upstreams []*udpUpStream
)

// 连接池大小，与 queryUpstream 中的 maxInflightPerUpstream 一致
const connPoolSize = 5

// bindToVirbr1 返回绑定到 virbr1 的 dialer
func bindToVirbr1() *net.Dialer {
	return &net.Dialer{
		KeepAliveConfig: net.KeepAliveConfig{Enable: false},
		// Control: func(network, address string, c syscall.RawConn) error {
		// 	return c.Control(func(fd uintptr) {
		// 		syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, "virbr1")
		// 	})
		// },
	}
}

// 程序启动时调用一次
func InitUpstreams(addrs []string) {
	dialer := bindToVirbr1()
	for _, a := range addrs {
		u := &udpUpStream{addr: a, pool: make(chan *dns.Conn, connPoolSize)}
		// 预创建连接池
		for i := 0; i < connPoolSize; i++ {
			conn := new(dns.Conn)
			netconn, err := dialer.DialContext(context.Background(), "udp", a)
			if err != nil {
				log.Printf("[upstream] 初始化连接失败 %s: %v", a, err)
				continue
			}
			conn.Conn = netconn
			u.pool <- conn
		}
		upstreams = append(upstreams, u)
	}
}

func UpdateUpstreams(addr string) {
	for _, a := range upstreams {
		if a.addr == addr {
			return // 已存在
		}
	}
	// 新增 upstream，初始化连接池
	InitUpstreams([]string{addr})
}

func cacheKeyFromReq(req *dns.Msg) string {
	if req == nil || len(req.Question) == 0 {
		return ""
	}
	var b strings.Builder
	for i, q := range req.Question {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strings.ToLower(q.Name))
		fmt.Fprintf(&b, "|%d|%d", q.Qtype, q.Qclass)
	}
	return b.String()
}

func queryUpstream(req *dns.Msg) (*dns.Msg, error) {
	cachekey := cacheKeyFromReq(req)
	// 1. 缓存
	if ans, _ := GetUpstreamCache().Get(cachekey); ans != nil {
		return ans, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(9 * time.Second):
			cancel()
			return
		}
	}()

	type result struct {
		reply *dns.Msg
		err   error
	}
	// ch := make(chan result, 1)
	// var wg sync.WaitGroup
	// for _, u := range upstreams {
	// 	u := u
	// 	wg.Add(1)
	// 	go func() {
	// 		defer wg.Done()
	// 		reply, err := queryOneUdp(ctx, u, req)
	// 		select {
	// 		case ch <- result{reply: reply, err: err}:
	// 			cancel()
	// 		default:
	// 		}
	// 	}()
	// }

	// // 等待第一个成功或超时
	// go func() { wg.Wait(); close(ch) }()

	// select {
	// case r := <-ch:
	// 	if r.err == nil {
	// 		GetUpstreamCache().Set(cachekey, r.reply)
	// 		return r.reply, nil
	// 	}
	// case <-ctx.Done():
	// }

	ch := make(chan result, len(upstreams))

	// 每个 upstream 的并发控制：最大同时飞行请求数
	const maxInflightPerUpstream = 5
	// 每次重试间隔（指数退避基础值）
	const baseRetryInterval = 50 * time.Millisecond

	var wg sync.WaitGroup

	for _, u := range upstreams {
		u := u
		wg.Add(1)

		go func() {
			defer wg.Done()

			// 信号量控制该 upstream 的并发数
			semaphore := make(chan struct{}, maxInflightPerUpstream)

			for attempt := 0; attempt < 20; attempt++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				// 获取信号量（阻塞直到有空位）
				semaphore <- struct{}{}

				go func(attemptNum int) {
					defer func() { <-semaphore }()

					reply, err := queryOneUdp(ctx, u, req)
					if err != nil {
						// 只有非超时错误才记录，超时是预期的
						if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "context canceled") {
							log.Printf("[upstream] %s attempt %d error: %v", u.addr, attemptNum, err)
						}
						return
					}
					// 成功：发送结果并取消其他请求（检查 ctx 避免无效发送）
					if ctx.Err() != nil {
						return
					}
					select {
					case ch <- result{reply: reply, err: nil}:
						GetUpstreamCache().Set(cachekey, reply)
						cancel()
					default:
					}
				}(attempt)

				// 指数退避：50ms, 100ms, 150ms... 避免 thundering herd
				sleepDuration := time.Duration(attempt+1) * baseRetryInterval
				if sleepDuration > 500*time.Millisecond {
					sleepDuration = 500 * time.Millisecond
				}

				select {
				case <-ctx.Done():
					return
				case <-time.After(sleepDuration):
				}
			}
		}()
	}

	// 不需要关闭 ch，wg 只用于确保 goroutine 收敛，ch 随函数返回被 GC

	select {
	case res := <-ch:
		if res.reply != nil {
			cancel()
			return res.reply, nil
		}
	case <-ctx.Done():
	}

	return nil, fmt.Errorf("all upstreams failed for %s", cachekey)
}

func isConnLost(err error) bool {
	// Direct EOF
	if err == io.EOF {
		return true
	}

	// net.OpError, which wraps syscall errors
	if opErr, ok := err.(*net.OpError); ok {
		// Check for syscall errors
		if sysErr, ok := opErr.Err.(syscall.Errno); ok {
			switch sysErr {
			case syscall.ECONNRESET, syscall.EPIPE, syscall.ETIMEDOUT, syscall.ENOTCONN:
				return true
			}
		}
		// Or string error matches
		msg := opErr.Err.Error()
		if strings.Contains(msg, "connection reset") ||
			strings.Contains(msg, "broken pipe") ||
			strings.Contains(msg, "use of closed network connection") {
			return true
		}
	}

	// String error matches (catch-all)
	msg := err.Error()
	if strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "use of closed network connection") {
		return true
	}

	return false
}

func queryOneUdp(ctx context.Context, u *udpUpStream, req *dns.Msg) (*dns.Msg, error) {
	// 设置读超时，防止对端半开
	client := &dns.Client{Net: "udp"}
	// client.UDPSize = 1300
	client.ReadTimeout = 8 * time.Second
	client.WriteTimeout = 5 * time.Second
	// client.Timeout = 10

	// 从连接池获取连接
	var conn *dns.Conn
	select {
	case conn = <-u.pool:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// 使用完后归还连接池
	defer func() { u.pool <- conn }()

	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(strings.ToLower(req.Question[0].Name)), req.Question[0].Qtype)
	m.RecursionDesired = true

	// select {
	// case <-ctx.Done():
	// 	return nil, ctx.Err()
	// default:
	// }
	// reply, _, err := client.ExchangeWithConn(m, conn)

	reply, _, err := client.ExchangeWithConnContext(ctx, m, conn)
	// log.Printf("[queryOneudp]  err %v %s", err, u.addr)
	if err != nil {
		// 连接可能断开，创建新连接替换
		if isConnLost(err) {
			conn.Conn.Close()
			// 异步重建连接放回池中
			go func() {
				dialer := bindToVirbr1()
				if newConn, err := dialer.DialContext(context.Background(), "udp", u.addr); err == nil {
					u.pool <- &dns.Conn{Conn: newConn}
				} else {
					log.Printf("[upstream] 重建连接失败 %s: %v", u.addr, err)
				}
			}()
			// 当前请求返回错误，由上层重试
		}
		return nil, err
	}
	return reply, nil
}

// /* 单次撞击，逻辑同上，但不再递归 */
// func queryUpstreamOnce(req *dns.Msg) (*dns.Msg, error) {
// 	cachekey := cacheKeyFromReq(req)
// 	m := new(dns.Msg)
// 	m.SetQuestion(dns.Fqdn(strings.ToLower(req.Question[0].Name)), req.Question[0].Qtype)
// 	m.RecursionDesired = true
// 	ctx, cancel := context.WithTimeout(context.Background(), 8000*time.Millisecond)
// 	defer cancel()

// 	ch := make(chan *dns.Msg, 1)
// 	var once sync.Once
// 	for _, u := range upstreams {
// 		u := u
// 		go func() {
// 			reply, err := queryOneUdp(ctx, u, req)
// 			log.Printf("[queryStreamOne] recv: %v, %v", reply, err)

// 			if err == nil && reply != nil {
// 				once.Do(func() {
// 					ch <- reply
// 					cancel()
// 				})
// 			}
// 		}()
// 	}
// 	select {
// 	case reply := <-ch:
// 		GetUpstreamCache().Set(cachekey, reply)
// 		return reply, nil
// 	case <-ctx.Done():
// 		return nil, fmt.Errorf("retry still failed for %s", cachekey)
// 	}
// }
