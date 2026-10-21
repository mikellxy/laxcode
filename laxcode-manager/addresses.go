package main

import "net/netip"

type addressRule struct {
	Prefix string
	Public uint32
}
type lpmKey struct {
	PrefixLen uint32
	Address   [16]byte
}

// Fixed destination policy, based on IANA special-purpose registries (2025-10-09).
// Deny transition mechanisms (NAT64, Teredo, 6to4) as well as non-global space.
// More-specific global anycast assignments remain usable. No runtime downloads.
var addressRules = []addressRule{
	{"0.0.0.0/0", 1}, {"2000::/3", 1},
	{"0.0.0.0/8", 0}, {"10.0.0.0/8", 0}, {"100.64.0.0/10", 0},
	{"127.0.0.0/8", 0}, {"169.254.0.0/16", 0}, {"172.16.0.0/12", 0},
	{"192.0.0.0/24", 0}, {"192.0.0.9/32", 1}, {"192.0.0.10/32", 1},
	{"192.0.2.0/24", 0}, {"192.88.99.0/24", 0}, {"192.168.0.0/16", 0},
	{"198.18.0.0/15", 0}, {"198.51.100.0/24", 0}, {"203.0.113.0/24", 0},
	{"224.0.0.0/3", 0},
	{"2001::/23", 0}, {"2001:1::1/128", 1}, {"2001:1::2/128", 1},
	{"2001:1::3/128", 1}, {"2001:3::/32", 1}, {"2001:4:112::/48", 1},
	{"2001:20::/28", 1}, {"2001:30::/28", 1},
	{"2001:db8::/32", 0}, {"2002::/16", 0}, {"3fff::/20", 0},
}

func prefixKey(raw string) lpmKey {
	p := netip.MustParsePrefix(raw).Masked()
	bits := p.Bits()
	if p.Addr().Is4() {
		bits += 96
	}
	return lpmKey{uint32(bits), addressBytes(p.Addr())}
}
