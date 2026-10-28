package main

import "net/netip"

// publicSignupSiteLimit bounds each public signup door independently, across
// clients and targets. Rejections keep the form's ordinary success response.
const publicSignupSiteLimit = 100

// rateLimitIP groups IPv6 clients by /64 so rotating interface addresses cannot
// evade a per-IP budget. IPv4 and mapped IPv4 retain their individual budget.
// This key is only for throttles; audit records keep the original client IP.
func rateLimitIP(raw string) string {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return raw
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}
