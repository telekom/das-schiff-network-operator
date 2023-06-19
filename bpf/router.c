// +build ignore

/* SPDX-License-Identifier: GPL-2.0 */
#include <linux/in.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>
#include <stddef.h>
#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/pkt_cls.h>
#include <linux/if_packet.h>
#include <linux/ip.h>
#include <linux/ipv6.h>
#include <linux/icmp.h>
#include <linux/icmpv6.h>
#include <linux/udp.h>
#include <linux/tcp.h>

#ifndef memcpy
#define memcpy(dest, src, n) __builtin_memcpy((dest), (src), (n))
#endif

#ifndef ctx_ptr
#define ctx_ptr(field) (void *)(long)(field)
#endif

#define EBPF_ROUTE 0
#define EBPF_ROUTENN 1
#define EBPF_ERPARSHDR 2
#define EBPF_NOT_FWD 3
#define EBPF_ERSTORMAC 4
#define EBPF_SIZE_EXC 5
#define EBPF_LAST_EXIT 6
#define EBPF_RES_MAX 7

#define BPF_FIB_LKUP_RET_MAX BPF_FIB_LKUP_RET_FRAG_NEEDED + 1

#ifndef AF_INET
#define AF_INET 2
#endif
#ifndef AF_INET6
#define AF_INET6 10
#endif

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, __u32);
	__type(value, __u32);
	__uint(max_entries, 256);
} lookup_port SEC(".maps");

struct datarec
{
	__u64 rx_packets;
	__u64 rx_bytes;
};

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(__u32));
	__uint(value_size, sizeof(struct datarec));
	__uint(max_entries, EBPF_RES_MAX);
} ebpf_ret_stats_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(__u32));
	__uint(value_size, sizeof(struct datarec));
	__uint(max_entries, BPF_FIB_LKUP_RET_MAX);
} ebpf_fib_lkup_stats_map SEC(".maps");

static __always_inline int ebpf_record_ret_stats(struct __sk_buff *skb, __u32 record, int tc_action)
{
	if (record >= EBPF_RES_MAX)
		return tc_action;

	/* Lookup in kernel BPF-side return pointer to actual data record */
	struct datarec *rec = bpf_map_lookup_elem(&ebpf_ret_stats_map, &record);
	if (!rec)
		return tc_action;

	rec->rx_packets++;
	rec->rx_bytes += (skb->data_end - skb->data);

	return tc_action;
}

static __always_inline void ebpf_record_fib_lkup(struct __sk_buff *skb, __u32 result)
{
	if (result >= BPF_FIB_LKUP_RET_MAX)
		return;

	/* Lookup in kernel BPF-side return pointer to actual data record */
	struct datarec *rec = bpf_map_lookup_elem(&ebpf_fib_lkup_stats_map, &result);
	if (!rec)
		return;

	rec->rx_packets++;
	rec->rx_bytes += (skb->data_end - skb->data);

	return;
}

static __always_inline __u32 bpf_get_interface(__u32 in)
{
	__u32 *lookup_index = bpf_map_lookup_elem(&lookup_port, &in);
	if (lookup_index != NULL)
	{
		return *lookup_index;
	}
	else
	{
		return in;
	}
}

static __always_inline int fill_fib_params_v4(struct __sk_buff *skb,
											  struct bpf_fib_lookup *fib_params)
{
	void *data_end = ctx_ptr(skb->data_end);
	void *data = ctx_ptr(skb->data);
	struct iphdr *ip4h;

	if (data + sizeof(struct ethhdr) > data_end)
		return -1;

	ip4h = (struct iphdr *)(data + sizeof(struct ethhdr));
	if ((void *)(ip4h + 1) > data_end)
		return -1;

	fib_params->family = AF_INET;
	fib_params->tos = ip4h->tos;
	fib_params->l4_protocol = ip4h->protocol;
	fib_params->sport = 0;
	fib_params->dport = 0;
	fib_params->tot_len = 0; // bpf_ntohs(ip4h->tot_len);
	fib_params->ipv4_src = ip4h->saddr;
	fib_params->ipv4_dst = ip4h->daddr;

	return 0;
}

static __always_inline int fill_fib_params_v6(struct __sk_buff *skb,
											  struct bpf_fib_lookup *fib_params)
{
	struct in6_addr *src = (struct in6_addr *)fib_params->ipv6_src;
	struct in6_addr *dst = (struct in6_addr *)fib_params->ipv6_dst;
	void *data_end = ctx_ptr(skb->data_end);
	void *data = ctx_ptr(skb->data);
	struct ipv6hdr *ip6h;

	if (data + sizeof(struct ethhdr) > data_end)
		return -1;

	ip6h = (struct ipv6hdr *)(data + sizeof(struct ethhdr));
	if ((void *)(ip6h + 1) > data_end)
		return -1;

	fib_params->family = AF_INET6;
	fib_params->flowinfo = 0;
	fib_params->l4_protocol = ip6h->nexthdr;
	fib_params->sport = 0;
	fib_params->dport = 0;
	fib_params->tot_len = 0; // bpf_ntohs(ip6h->payload_len);
	*src = ip6h->saddr;
	*dst = ip6h->daddr;

	return 0;
}

static __always_inline int tc_redir(struct __sk_buff *skb)
{
	struct bpf_fib_lookup fib_params = {};
	__u8 zero[ETH_ALEN * 2];
	int ret = -1;

	switch (skb->protocol)
	{
	case __bpf_constant_htons(ETH_P_IP):
		ret = fill_fib_params_v4(skb, &fib_params);
		break;
	case __bpf_constant_htons(ETH_P_IPV6):
		ret = fill_fib_params_v6(skb, &fib_params);
		break;
	}

	if (ret)
		return ebpf_record_ret_stats(skb, EBPF_ERPARSHDR, TC_ACT_OK);

	fib_params.ifindex = bpf_get_interface(skb->ingress_ifindex);

	ret = bpf_fib_lookup(skb, &fib_params, sizeof(fib_params), 0);
	ebpf_record_fib_lkup(skb, ret);
	if (ret == BPF_FIB_LKUP_RET_NOT_FWDED || ret < 0)
		return ebpf_record_ret_stats(skb, EBPF_NOT_FWD, TC_ACT_OK);

	__builtin_memset(&zero, 0, sizeof(zero));
	if (bpf_skb_store_bytes(skb, 0, &zero, sizeof(zero), 0) < 0)
		return ebpf_record_ret_stats(skb, EBPF_ERSTORMAC, TC_ACT_SHOT);

	if (ret == BPF_FIB_LKUP_RET_SUCCESS)
	{
		void *data_end = ctx_ptr(skb->data_end);
		struct ethhdr *eth = ctx_ptr(skb->data);

		if ((void *)(eth + 1) > data_end)
			return ebpf_record_ret_stats(skb, EBPF_SIZE_EXC, TC_ACT_SHOT);

		__builtin_memcpy(eth->h_dest, fib_params.dmac, ETH_ALEN);
		__builtin_memcpy(eth->h_source, fib_params.smac, ETH_ALEN);

		ebpf_record_ret_stats(skb, EBPF_ROUTE, 0);
		return bpf_redirect(fib_params.ifindex, 0);
	}
	else if (ret == BPF_FIB_LKUP_RET_NO_NEIGH)
	{
		struct bpf_redir_neigh nh_params = {};

		nh_params.nh_family = fib_params.family;
		__builtin_memcpy(&nh_params.ipv6_nh, &fib_params.ipv6_dst,
						 sizeof(nh_params.ipv6_nh));

		ebpf_record_ret_stats(skb, EBPF_ROUTENN, 0);
		return bpf_redirect_neigh(fib_params.ifindex, &nh_params,
								  sizeof(nh_params), 0);
	}

	return ebpf_record_ret_stats(skb, EBPF_LAST_EXIT, TC_ACT_SHOT);
}

SEC("tc_router")
int tc_router_func(struct __sk_buff *skb)
{
	return tc_redir(skb);
}

char _license[] SEC("license") = "GPL";
