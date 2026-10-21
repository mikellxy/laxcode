#include "quota.h"

HASH(metadata, __u32, struct metadata, 1);
HASH(cgroup_owner, __u64, __u64, 8192);
HASH(user_bucket, __u64, __u64, 4096);
HASH(bucket_meta, __u64, struct bucket_meta, 8192);
HASH(grants, __u64, struct grant, 8192);
HASH(usage, __u64, struct usage, 8192);
HASH(control_peers, struct peer, __u32, 64);
struct {
    __uint(type, 11); __uint(max_entries, 128); __uint(map_flags, 1);
    __type(key, struct prefix); __type(value, __u32);
} address_class SEC(".maps");
struct { __uint(type, 27); __uint(max_entries, 65536); } events SEC(".maps");
struct {
    __uint(type, 6); __uint(max_entries, 8);
    __type(key, __u32); __type(value, __u64);
} diagnostics SEC(".maps");

INLINE void diagnostic(__u32 reason) {
    __u64 *n = bpf_map_lookup_elem(&diagnostics, &reason);
    if (n) (*n)++;
}
INLINE __u16 be16(const __u8 *p) { return ((__u16)p[0] << 8) | p[1]; }

/* Fail closed on fragments, unknown extension chains, or truncated transport.
 * Supervisor TCP/DNS needs neither raw fragments nor arbitrary IP protocols. */
INLINE int parse(struct __sk_buff *skb, int ingress, struct peer *peer) {
    __u8 h[40] = {};
    if (bpf_skb_load_bytes(skb, 0, h, 20)) return 0;
    __u32 offset = 0;
    if ((h[0] >> 4) == 4) {
        offset = (h[0] & 15) * 4;
        if (offset < 20 || offset > 60 || (be16(h + 6) & 0x3fff)) return 0;
        peer->address[10] = 0xff; peer->address[11] = 0xff;
        if (ingress) __builtin_memcpy(peer->address + 12, h + 12, 4);
        else __builtin_memcpy(peer->address + 12, h + 16, 4);
        peer->proto = h[9];
    } else if ((h[0] >> 4) == 6) {
        if (bpf_skb_load_bytes(skb, 0, h, 40)) return 0;
        if (ingress) __builtin_memcpy(peer->address, h + 8, 16);
        else __builtin_memcpy(peer->address, h + 24, 16);
        peer->proto = h[6]; offset = 40;
        /* Hop-by-hop and destination options only; no routing/NAT64 tunnels. */
        for (int i = 0; i < 4; i++) {
            if (peer->proto != 0 && peer->proto != 60) break;
            __u8 ext[2];
            if (bpf_skb_load_bytes(skb, offset, ext, 2)) return 0;
            peer->proto = ext[0]; offset += ((__u32)ext[1] + 1) * 8;
        }
    } else return 0;
    if (peer->proto != 6 && peer->proto != 17) return 0;
    __u8 transport[20] = {};
    if (peer->proto == 6) {
        if (bpf_skb_load_bytes(skb, offset, transport, 20)) return 0;
        __u32 size = (transport[12] >> 4) * 4;
        if (size < 20 || offset + size > skb->len) return 0;
    } else {
        if (bpf_skb_load_bytes(skb, offset, transport, 8)) return 0;
        if (be16(transport + 4) < 8) return 0;
    }
    peer->port = be16(transport + (ingress ? 0 : 2));
    return 1;
}

INLINE int account(struct __sk_buff *skb, int ingress) {
    struct peer peer = {};
    if (!parse(skb, ingress, &peer)) { diagnostic(0); return 0; }
    __u32 zero = 0;
    struct metadata *meta = bpf_map_lookup_elem(&metadata, &zero);
    if (!meta || !meta->ready) { diagnostic(1); return 0; }
    if (bpf_map_lookup_elem(&control_peers, &peer)) return 1;
    struct prefix prefix = {.prefixlen = 128};
    __builtin_memcpy(prefix.address, peer.address, 16);
    __u32 *public = bpf_map_lookup_elem(&address_class, &prefix);
    if (!public || !*public) { diagnostic(2); return 0; }
    __u64 cg = bpf_skb_cgroup_id(skb);
    __u64 *user = bpf_map_lookup_elem(&cgroup_owner, &cg);
    if (!cg || !user) { diagnostic(3); return 0; }
    __u64 user_id = *user;
    __u64 *bucket = bpf_map_lookup_elem(&user_bucket, &user_id);
    if (!bucket) { diagnostic(3); return 0; }
    __u64 bucket_id = *bucket;
    struct bucket_meta *bm = bpf_map_lookup_elem(&bucket_meta, &bucket_id);
    struct grant *g = bpf_map_lookup_elem(&grants, &bucket_id);
    struct usage *u = bpf_map_lookup_elem(&usage, &bucket_id);
    __u64 now = bpf_ktime_get_boot_ns();
    if (!bm || !g || !u || bm->user != user_id || now >= bm->deadline_ns) { diagnostic(4); return 0; }
    __u64 limit = g->bytes, version = g->version, low = meta->low_watermark;
    __u32 len = skb->len;
    int pass = 0, notify = 0;
    bpf_spin_lock(&u->lock);
    __u64 used = u->rx + u->tx;
    if (used >= u->rx && used <= limit && len <= limit - used) {
        if (ingress) u->rx += len; else u->tx += len;
        used += len; pass = 1;
    } else {
        u->drop_bytes += len; u->drop_packets++;
    }
    if ((used >= limit || limit - used <= low) &&
        (u->notified_version != version || now - u->last_event_ns >= 1000000000ULL)) {
        u->notified_version = version; u->last_event_ns = now; notify = 1;
    }
    bpf_spin_unlock(&u->lock);
    if (notify) {
        struct event event = {bucket_id, version};
        if (bpf_ringbuf_output(&events, &event, sizeof(event), 0)) diagnostic(5);
    }
    if (!pass) diagnostic(6);
    return pass;
}
SEC("cgroup_skb/ingress") int ingress(struct __sk_buff *skb) { return account(skb, 1); }
SEC("cgroup_skb/egress") int egress(struct __sk_buff *skb) { return account(skb, 0); }
char LICENSE[] SEC("license") = "GPL";
