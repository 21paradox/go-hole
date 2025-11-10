package main

import (
	"log"

	"github.com/miekg/dns"
)

// func parseQuery(source net.Addr, m *dns.Msg) (m1 *dns.Msg) {
// 	for _, q := range m.Question {
// 		name := strings.ToLower(q.Name)
// 		// res, errCode := processDnsQuery(name, q.Qtype, source)
// 		// m.Answer = append(m.Answer, res...)
// 		// m.Rcode = errCode
// 		qtype := q.Qtype

// 		arr, err := queryLocal(name, qtype)
// 		if err == nil {
// 			logQueryResult(source, name, qtype, "resolved as local address")
// 			m.Answer = append(m.Answer, arr...)
// 			m.Rcode = dns.RcodeSuccess
// 		}

// 		arr, err = queryBlacklist(name, qtype)
// 		if err == nil {
// 			logQueryResult(source, name, qtype, "resolved as blacklisted name")
// 			return arr, dns.RcodeNameError
// 		}
// 		reply, err1 := queryUpstream(name, qtype)
// 		if err1 == nil {
// 			logQueryResult(source, name, qtype, "resolved via upstream")
// 			return reply.Answer, reply.Rcode
// 		}
// 		logQueryResult(source, name, qtype, "did not resolve")
// 		return []dns.RR{}, dns.RcodeNameError
// 	}

// }

// func processDnsQuery(name string, qtype uint16, source net.Addr) ([]dns.RR, int) {
// 	arr, err := queryLocal(name, qtype)
// 	if err == nil {
// 		logQueryResult(source, name, qtype, "resolved as local address")
// 		return arr, dns.RcodeSuccess
// 	}
// 	arr, err = queryBlacklist(name, qtype)
// 	if err == nil {
// 		logQueryResult(source, name, qtype, "resolved as blacklisted name")
// 		return arr, dns.RcodeNameError
// 	}
// 	reply, err1 := queryUpstream(name, qtype)
// 	if err1 == nil {
// 		logQueryResult(source, name, qtype, "resolved via upstream")
// 		return reply.Answer, reply.Rcode
// 	}
// 	logQueryResult(source, name, qtype, "did not resolve")
// 	return []dns.RR{}, dns.RcodeNameError
// }

func handleDnsRequest(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Compress = false

	switch r.Opcode {
	case dns.OpcodeQuery:
		if len(m.Question) == 0 {
			m.Answer = append(m.Answer, []dns.RR{}...)
			m.Rcode = dns.RcodeNameError
		} else {
			name := r.Question[0].Name
			qtype := r.Question[0].Qtype
			source := w.RemoteAddr()

			arr, err := queryLocal(name, qtype)
			if err == nil {
				logQueryResult(source, name, qtype, "resolved as local address")
				m.Answer = append(m.Answer, arr...)
				break
			}
			arr, err = queryBlacklist(name, qtype)
			if err == nil {
				logQueryResult(source, name, qtype, "resolved as blacklisted name")
				m.Answer = append(m.Answer, arr...)
				break
			}

			out, err := queryUpstream(m)
			if err == nil {
				out1 := out.Copy()
				out1.SetReply(r)
				m = out1
			} else {
				m.Answer = append(m.Answer, []dns.RR{}...)
				m.Rcode = dns.RcodeNameError
			}
		}
	}

	w.WriteMsg(m)
	w.Close()
}

func listenAndServe() {
	dns.HandleFunc(".", handleDnsRequest)

	addrs := GetConfig().ListenAddr
	ups := GetConfig().UpstreamDNS
	InitUpstreams(ups)

	for _, addr := range addrs {
		addr := addr
		server := &dns.Server{
			// Addr: GetConfig().ListenAddr,
			Addr: addr,
			Net:  "udp",
		}
		log.Printf("Starting at %s\n", addr)
		go func() {
			err := server.ListenAndServe()
			defer server.Shutdown()
			if err != nil {
				log.Fatalf("Failed to start server: %s\n ", err.Error())
			}
		}()
	}

	select {}
}
