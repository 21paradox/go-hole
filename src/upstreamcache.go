package main

import (
	"errors"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"github.com/miekg/dns"
)

type UpstreamCache struct {
	Cache *ttlcache.Cache[string, *dns.Msg]
}

var UpstreamCacheInstance *UpstreamCache = &UpstreamCache{}

func GetUpstreamCache() *UpstreamCache {
	return UpstreamCacheInstance
}

func (c *UpstreamCache) Init() {
	c.Cache = ttlcache.New(
		ttlcache.WithDisableTouchOnHit[string, *dns.Msg](),
	)
	go c.Cache.Start()
}

func (c *UpstreamCache) Set(name string, reply *dns.Msg) {
	ttl := time.Duration(c.getMinTtl(reply)) * time.Second
	// c.Cache.Set(c.getKey(name, qtype), reply, ttl)
	c.Cache.Set(name, reply, ttl)
}

func (c *UpstreamCache) Get(name string) (*dns.Msg, error) {
	// res := c.Cache.Get(c.getKey(name, qtype))
	res := c.Cache.Get(name)
	if res == nil || res.IsExpired() {
		return nil, errors.New("record not found in cache")
	}
	return res.Value(), nil
}

func (c *UpstreamCache) Clear() {
	c.Cache.DeleteAll()
}

// func (c *UpstreamCache) getKey(name string, qtype uint16) string {
// 	return name + "_" + strconv.Itoa(int(qtype))
// }

func (c *UpstreamCache) getMinTtl(reply *dns.Msg) uint32 {
	var res uint32 = 1800 // Default: 30 minutes
	for _, record := range reply.Answer {
		if record.Header().Ttl < res {
			res = record.Header().Ttl
		}
	}
	return res
}
