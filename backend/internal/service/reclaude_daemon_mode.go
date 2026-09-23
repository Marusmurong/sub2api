package service

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ExtraKeyReclaudeDaemonProxy 指向本机 reclaude 客户端 daemon 的正向代理地址
// （形如 http://127.0.0.1:36159，端口见 ~/.reclaude/state.json 的 daemon.port）。
//
// 配置了它的账号走 **daemon 模式**：内层请求经该代理原样发往 api.anthropic.com，
// 由真客户端负责封信封、ed25519 签名、选节点、打 rec 网关。
//
// 🔴 为什么需要这条路径：自研信封实现过不了对方的校验。2026-09-24 逐项验证过
// 签名算法、canonical 串、base64 变体、sha256 覆盖范围、信封二进制结构、meta
// 字段与 header 取值，全部正确 —— 同一串信封字节离线重签重放能拿 200，经
// sub2api 发出却被拒为 bad_envelope。daemon 模式绕开整层协议实现，
// 也不会因对方改协议而再次失效。
//
// 我们仍然负责 CC 伪装（billing block / cc_version 指纹 / beta 模板 / UA）——
// rec 网关强制只服务 Claude Code 客户端，这本来就是 sub2api 的本职。
const ExtraKeyReclaudeDaemonProxy = "reclaude_daemon_proxy"

// ExtraKeyReclaudeDaemonEndpoint 指向 daemon 的 **transparent** 口
// （state.json 的 daemon.transparent_port，形如 https://127.0.0.1:40997）。
//
// 🔴 与正向代理口（daemon.port）的区别，2026-09-24 实测：
//   - 正向代理口走 CONNECT：daemon 接受连接，但转发后回 400 bad_envelope
//   - transparent 口直连 TLS：请求正常抵达 rec 网关
//
// Claude Code 本体打的就是 transparent 口（其二进制里硬编码 127.0.0.1:<port>）。
const ExtraKeyReclaudeDaemonEndpoint = "reclaude_daemon_endpoint"

// ReclaudeDaemonProxy 返回该账号的 daemon 代理地址；未配置或不合法时返回 false。
//
// 🔴 只接受回环地址。daemon 是本机进程，允许任意主机 = 把带完整凭据、
// 全量明文 prompt 的请求发给任意地址。
func ReclaudeDaemonProxy(account *Account) (string, bool) {
	if account == nil || !account.IsReclaude() {
		return "", false
	}
	raw := strings.TrimSpace(credentialString(account.Extra, ExtraKeyReclaudeDaemonProxy))
	if raw == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	if !isLoopbackHost(parsed.Hostname()) {
		return "", false
	}
	return raw, true
}

// isLoopbackHost 判断主机名是否指向本机。
//
// 只认字面回环：域名解析结果可变（DNS 重绑定），而这条链路承载明文 prompt。
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// normalizeInnerRequestURLForDaemon 补齐内层请求 URL 缺失的 scheme。
//
// 🔴 sub2api 构造的内层请求 URL 形如 `//api.anthropic.com/v1/messages`（无 scheme）。
// 信封路径不受影响 —— 那里只把 URL 当字符串塞进 meta；但把 daemon 当正向代理时，
// Go 会据此发出 `CONNECT //api.anthropic.com:443`（两个斜杠），daemon 收到畸形
// 目标后封装失败，回 400 bad_envelope。
//
// 该错误极具误导性：长得像信封格式问题，实际只是 URL 少了 "https:"。
func normalizeInnerRequestURLForDaemon(req *http.Request) {
	if req == nil || req.URL == nil || req.URL.Scheme != "" {
		return
	}
	// 只补 https：这条链路的对端固定是 Anthropic API。
	req.URL.Scheme = "https"
}

// ReclaudeDaemonEndpoint 返回 daemon 的 transparent 端点；未配置时返回 false。
func ReclaudeDaemonEndpoint(account *Account) (string, bool) {
	if account == nil || !account.IsReclaude() {
		return "", false
	}
	raw := strings.TrimSpace(credentialString(account.Extra, ExtraKeyReclaudeDaemonEndpoint))
	if raw == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || !isLoopbackHost(parsed.Hostname()) {
		return "", false
	}
	return raw, true
}

// retargetToReclaudeDaemon 把内层请求改指到 daemon 的 transparent 端点。
//
// 🔴 只改 URL 的 scheme/host，**保留 Host 头**：daemon 靠 Host 判断这个请求
// 该转发到哪个上游，改掉它 daemon 就不知道目标是 api.anthropic.com。
func retargetToReclaudeDaemon(req *http.Request, endpoint string) error {
	if req == nil || req.URL == nil {
		return fmt.Errorf("reclaude daemon: request is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || !isLoopbackHost(parsed.Hostname()) {
		return fmt.Errorf("reclaude daemon: endpoint must be a loopback URL, got %q", endpoint)
	}

	// 原目标主机在改写前保存进 Host 头。
	if req.Host == "" {
		req.Host = req.URL.Host
	}
	req.URL.Scheme = parsed.Scheme
	req.URL.Host = parsed.Host
	return nil
}
