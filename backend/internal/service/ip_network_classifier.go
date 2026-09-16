package service

import "context"

// IPNetworkClassifier 判定一个公网 IP 的网络类型（住宅 / 机房 / VPN …）。
//
// 实现方直连数据源，不经过被测代理：我们分类的是代理**出口 IP**，该 IP 已由
// ProxyExitInfoProber 探测得到，再走一次代理既无意义，又白白消耗它的流量与配额。
//
// 拿不到判定结果时返回 error，调用方据此区分「问不到」与「问了但归不了类」——
// 两者在质量检测里的处理完全不同，见 CheckProxyQuality。
type IPNetworkClassifier interface {
	ClassifyIP(ctx context.Context, ip string) (*IPNetworkInfo, error)
}
