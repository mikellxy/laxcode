/* Minimal fixed Linux UAPI subset. No kernel-private layouts or CO-RE needed. */
#ifndef LAXCODE_QUOTA_H
#define LAXCODE_QUOTA_H
typedef unsigned char __u8;
typedef unsigned short __u16;
typedef unsigned int __u32;
typedef unsigned long long __u64;
struct __sk_buff { __u32 len, pkt_type, mark, queue_mapping, protocol; };
struct bpf_spin_lock { __u32 val; };
#define SEC(name) __attribute__((section(name), used))
#define __uint(name, val) int (*name)[val]
#define __type(name, val) val *name
#define INLINE static __attribute__((always_inline)) inline
#define HASH(name, key_t, value_t, count) \
struct { __uint(type, 1); __uint(max_entries, count); __type(key, key_t); __type(value, value_t); } name SEC(".maps")
static void *(*bpf_map_lookup_elem)(void *, const void *) = (void *)1;
static long (*bpf_skb_load_bytes)(const void *, __u32, void *, __u32) = (void *)26;
static __u64 (*bpf_skb_cgroup_id)(void *) = (void *)79;
static long (*bpf_spin_lock)(struct bpf_spin_lock *) = (void *)93;
static long (*bpf_spin_unlock)(struct bpf_spin_lock *) = (void *)94;
static __u64 (*bpf_ktime_get_boot_ns)(void) = (void *)125;
static long (*bpf_ringbuf_output)(void *, void *, __u64, __u64) = (void *)130;

struct peer { __u8 address[16]; __u16 port; __u8 proto, pad; };
struct prefix { __u32 prefixlen; __u8 address[16]; };
struct grant { __u64 bytes, version; };
struct bucket_meta { __u64 user, deadline_ns; };
struct usage {
    struct bpf_spin_lock lock;
    __u32 pad;
    __u64 rx, tx, drop_bytes, drop_packets, notified_version, last_event_ns;
};
struct event { __u64 bucket, version; };
struct metadata {
    __u32 abi, ready;
    __u8 db[16], config_hash[32];
    __u64 parent_id, low_watermark;
};
_Static_assert(sizeof(struct peer) == 20, "peer ABI");
_Static_assert(sizeof(struct usage) == 56, "usage ABI");
_Static_assert(sizeof(struct metadata) == 72, "metadata ABI");
#endif
