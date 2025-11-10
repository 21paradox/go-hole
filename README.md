# Go-hole
[![](https://img.shields.io/github/v/release/virtualzone/go-hole)](https://github.com/virtualzone/go-hole/releases)
[![](https://img.shields.io/github/release-date/virtualzone/go-hole)](https://github.com/virtualzone/go-hole/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/virtualzone/go-hole)](https://goreportcard.com/report/github.com/virtualzone/go-hole)
[![](https://img.shields.io/github/license/virtualzone/go-hole)](https://github.com/virtualzone/go-hole/blob/master/LICENSE)

Minimalistic DNS server which serves as an upstream proxy and ad blocker. Written in Go, inspired by [Pi-hole®](https://github.com/pi-hole/pi-hole).

- 增加了并行请求dns的功能，适用于高丢包环境。
- 并发请求上游dns服务器，如果有一个返回了，取消其他pending的请求。
- 默认缓存dns结果30分钟

## Features
* Minimalistic DNS server, written in Golang, optimized for high performance
* Blacklist DNS names via user-specific source lists
* Whitelist DNS names that are actually blacklisted
* Multiple user-settable upstream DNS servers
* Caching of upstream query results
* Local name resolution
* Pre-built, minimalistic Docker image

## How it works
Go-hole serves as DNS server on your (home) network. Instead of having your clients sending DNS queries directly to the internet or to your router, they are resolved by your local Go-hole instance. Go-hole sends these queries to one or more upstream DNS servers and caches the upstream query results for maximum performance.

Incoming queries from your clients are checked against a list of unwanted domain names ("blacklist"), such as well-known ad serving domains and trackers. If a requested name matches a name on the blacklist, Go-hole responds with error code NXDOMAIN (non-existing domain). This leads to clients not being able to load ads and tracker codes. In case you want to access a blacklisted domain, you can easily add it to a whitelist.

As an additional feature, you can set a list of custom hostnames/domain names to be resolved to specific IP addresses. This is useful for accessing services on your local network by name instead of their IP addresses.


## 构建
go build -ldflags="-w -s" -o ./gohole src/*.go

## 配置 config.yml
参考项目的config.yaml

## 运行
./gohole