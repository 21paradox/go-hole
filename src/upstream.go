package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

/* ---------- 1. 每个 upstream 的“可重连”连接 ---------- */
type udpUpStream struct {
	addr string
	// conn *dns.Conn // 长连接
}

/* ---------- 2. 全局 upstream 池 ---------- */
var (
	upstreams []*udpUpStream
)

// 程序启动时调用一次
func InitUpstreams(addrs []string) {
	for _, a := range addrs {
		// conn, err := dns.Dial("udp", a)
		// if err != nil {
		upstreams = append(upstreams, &udpUpStream{addr: a})
		// }
	}
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

	ch := make(chan result, 5)
	reqCount := atomic.Int32{}
	reqCount.Store(0)
	var requestFn func(ctx context.Context, uc *udpUpStream)
	requestFn = func(ctx context.Context, uc *udpUpStream) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		reply, err := queryOneUdp(ctx, uc, req)
		select {
		case ch <- result{reply: reply, err: err}:
		default:
		}
		reqCount.Add(1)
		if err != nil {
			if strings.Contains(err.Error(), "timeout") {
			} else {
				log.Printf("[upstream] %s [domain] send error: %v", uc.addr, err)
			}
			return
		}
	}

	for _, u := range upstreams {
		// log.Printf("[upstreamloop] %s use addr: ", u.addr)
		u := u

		go requestFn(ctx, u)
		go func() {
			u1 := u
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
					if reqCount.Load() > int32(len(upstreams)*4) {
						log.Printf("[upstream] %s count check > 10", u.addr)
						cancel()
						return
					}
					go requestFn(ctx, u1)
				}
			}
		}()
	}

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

	// conn, err := dns.Dial("udp", u.addr)
	dialer := net.Dialer{
		KeepAliveConfig: net.KeepAliveConfig{
			Enable: false,
		},
	}

	conn := new(dns.Conn)
	netconn, err := dialer.DialContext(ctx, "udp", u.addr)
	if err != nil {
		return nil, err
	}
	conn.Conn = netconn

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
		return nil, err
	}
	return reply, nil
}

// /* 单次撞击，逻辑同上，但不再递归 */
func queryUpstreamOnce(req *dns.Msg) (*dns.Msg, error) {
	cachekey := cacheKeyFromReq(req)
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(strings.ToLower(req.Question[0].Name)), req.Question[0].Qtype)
	m.RecursionDesired = true
	ctx, cancel := context.WithTimeout(context.Background(), 8000*time.Millisecond)
	defer cancel()

	ch := make(chan *dns.Msg, 1)
	var once sync.Once
	for _, u := range upstreams {
		u := u
		go func() {
			c := &dns.Client{Timeout: 5000 * time.Millisecond}
			conn, err := dns.Dial("udp", u.addr)
			reply, _, err := c.ExchangeWithConnContext(ctx, m, conn)
			log.Printf("[queryStreamOne] recv: %v, %v", reply, err)

			if err == nil && reply != nil {
				once.Do(func() {
					ch <- reply
					cancel()
				})
			}
		}()
	}
	select {
	case reply := <-ch:
		GetUpstreamCache().Set(cachekey, reply)
		return reply, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("retry still failed for %s", cachekey)
	}
}
