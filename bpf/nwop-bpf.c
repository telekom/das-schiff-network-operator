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

	ret = bpf_fib_lookup(skb, &fib_params, sizeof(fib_params), BPF_FIB_LOOKUP_DIRECT);
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

struct
{
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 24); // 16 MB
} neighbor_ringbuf SEC(".maps");

// Max number of ICMPv6 NA options to scan to find TLLA
#define NA_MAX_OPTS 6

struct neighbor_event
{
    __u32 ifindex;
    __u8 family; // 4 for IPv4, 6 for IPv6
    __u8 mac[6];
    __u8 ip[16]; // IPv4 in first 4 bytes, rest zero
};

// Minimal vlan header definition (avoid kernel headers not available to BPF C)
struct vlan_hdr
{
    __be16 h_vlan_TCI;
    __be16 h_vlan_encapsulated_proto;
};

// Minimal ARP header (Ethernet/IPv4)
struct arp_eth_ipv4
{
    __be16 htype;
    __be16 ptype;
    __u8 hlen;
    __u8 plen;
    __be16 oper;
    __u8 sha[6];
    __u8 spa[4];
    __u8 tha[6];
    __u8 tpa[4];
};

static __always_inline int parse_eth_tc(struct __sk_buff *skb, void **data, void **data_end, __u16 *proto, __u64 *off)
{
    void *d = ctx_ptr(skb->data);
    void *de = ctx_ptr(skb->data_end);
    if (d + sizeof(struct ethhdr) > de)
        return -1;
    struct ethhdr *eth = d;
    *proto = bpf_ntohs(eth->h_proto);
    *off = sizeof(struct ethhdr);

// Handle single VLAN tag if present
#ifndef ETH_P_8021AD
#define ETH_P_8021AD 0x88A8
#endif
    if (*proto == ETH_P_8021Q || *proto == ETH_P_8021AD)
    {
        if (d + *off + sizeof(struct vlan_hdr) > de)
            return -1;
        struct vlan_hdr *vh = d + *off;
        *proto = bpf_ntohs(vh->h_vlan_encapsulated_proto);
        *off += sizeof(struct vlan_hdr);
    }

    *data = d;
    *data_end = de;
    return 0;
}

static __always_inline void emit_event(__u32 ifindex, __u8 family, const __u8 mac[6], const __u8 ip[16])
{
    struct neighbor_event *ev = bpf_ringbuf_reserve(&neighbor_ringbuf, sizeof(*ev), 0);
    if (!ev)
        return;
    ev->ifindex = ifindex;
    ev->family = family;
    __builtin_memcpy(ev->mac, mac, 6);
    __builtin_memcpy(ev->ip, ip, 16);
    bpf_ringbuf_submit(ev, 0);
}

SEC("tc/neighbor_reply")
int handle_neighbor_reply_tc(struct __sk_buff *skb)
{
    void *data, *data_end;
    __u16 proto = 0;
    __u64 off = 0;
    if (parse_eth_tc(skb, &data, &data_end, &proto, &off) < 0)
        return TC_ACT_OK;

    // Prepare buffers
    __u8 mac[6] = {};
    __u8 ip[16] = {};

    if (proto == ETH_P_ARP)
    {
        if (data + off + sizeof(struct arp_eth_ipv4) > data_end)
            return TC_ACT_OK;
        struct arp_eth_ipv4 *arp = data + off;
        if (arp->hlen != 6 || arp->plen != 4)
            return TC_ACT_OK;
        
        __u16 oper = bpf_ntohs(arp->oper);
        // Handle ARP Request (oper == 1) and ARP Reply (oper == 2)
        if (oper != 1 && oper != 2)
            return TC_ACT_OK;

        // Check for Gratuitous ARP according to RFC 5944 section 4.6:
        // Both ARP Sender Protocol Address and ARP Target Protocol Address
        // are set to the same IP address (the address being announced)
        
        // Simple memory comparison for IPv4 addresses (4 bytes)
        __u8 spa_bytes[4], tpa_bytes[4];
        __builtin_memcpy(spa_bytes, arp->spa, 4);
        __builtin_memcpy(tpa_bytes, arp->tpa, 4);
        
        // Compare the 4 bytes manually since memcmp might not be available
        __u8 is_gratuitous = 1;
        #pragma unroll
        for (int i = 0; i < 4; i++) {
            if (spa_bytes[i] != tpa_bytes[i]) {
                is_gratuitous = 0;
                break;
            }
        }
        
        if (is_gratuitous) {
            // Handle Gratuitous ARP
            // For gratuitous ARP, extract the correct MAC address:
            // - ARP Request: use Sender Hardware Address (sha)  
            // - ARP Reply: use Target Hardware Address (tha) as per RFC 5944 section 4.6
            if (oper == 1) {
                // ARP Request: THA field is not used, get MAC from SHA
                __builtin_memcpy(mac, arp->sha, 6);
            } else {
                // ARP Reply: THA is set to the link-layer address for cache update
                __builtin_memcpy(mac, arp->tha, 6);
            }
            
            // IPv4 goes in first 4 bytes of ip buffer (use spa since spa == tpa)
            __builtin_memcpy(ip, arp->spa, 4);
            emit_event(skb->ifindex, 4 /* IPv4 */, mac, ip);
        } else if (oper == 2) {
            // Handle non-gratuitous ARP Reply
            // In a regular ARP reply, the sender is replying with their own MAC/IP mapping
            // Learn from the ARP Sender Hardware Address and Protocol Address
            __builtin_memcpy(mac, arp->sha, 6);
            __builtin_memcpy(ip, arp->spa, 4);
            emit_event(skb->ifindex, 4 /* IPv4 */, mac, ip);
        }
        // Note: We don't process non-gratuitous ARP requests as they don't provide
        // definitive MAC-to-IP mappings (the sender might not own the target IP)
    }
    else if (proto == ETH_P_IPV6)
    {
        if (data + off + sizeof(struct ipv6hdr) > data_end)
            return TC_ACT_OK;
        struct ipv6hdr *ip6 = data + off;
        if (ip6->nexthdr != IPPROTO_ICMPV6)
            return TC_ACT_OK;
        __u64 off2 = off + sizeof(struct ipv6hdr);
        // Minimal ICMPv6 header check
        if (data + off2 + sizeof(struct icmp6hdr) > data_end)
            return TC_ACT_OK;
        struct icmp6hdr *icmp6 = data + off2;
        // Neighbor Advertisement type = 136
        if (icmp6->icmp6_type != 136)
            return TC_ACT_OK;

        // After icmp6hdr comes the 16-byte target address directly
        unsigned char *pos = (unsigned char *)((void *)icmp6 + sizeof(struct icmp6hdr));
        if ((void *)pos + 16 > data_end)
            return TC_ACT_OK;
        __builtin_memcpy(ip, pos, 16);
        pos += 16;

        // Parse up to NA_MAX_OPTS options to find Target Link-Layer Address (type 2)
        __u8 emitted = 0;
#pragma unroll
        for (int i = 0; i < NA_MAX_OPTS; i++)
        {
            if (emitted)
                break;
            // Ensure we can read type and length without OOB
            if ((void *)pos + 2 > data_end)
                break;
            __u8 hdr[2];
            __builtin_memcpy(hdr, pos, 2);
            __u8 opt_type = hdr[0];
            __u8 opt_len_units = hdr[1];
            if (opt_len_units == 0)
                break;
            // Cap units to a sane bound to help the verifier reason about opt_len
            if (opt_len_units > 32)
                break;
            __u64 opt_len = (__u64)opt_len_units * 8;
            // Ensure whole option is within bounds before accessing its body
            if ((void *)pos + opt_len > data_end)
                break;

            if (opt_type == 2 /* TLLA */)
            {
                if (opt_len >= 8 && (void *)pos + 8 <= data_end)
                {
                    __u8 mac6[6] = {};
                    __builtin_memcpy(mac6, (void *)pos + 2, 6);
                    emit_event(skb->ifindex, 6 /* IPv6 */, mac6, ip);
                    emitted = 1;
                }
                break; // stop after TLLA processing
            }

            // Move to next option
            pos += opt_len;
        }
    }

    return TC_ACT_OK;
}

char _license[] SEC("license") = "GPL";
