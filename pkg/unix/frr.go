package unix

const (
	// https://github.com/FRRouting/frr/blob/master/zebra/rt_netlink.h#L40-L47
	RTPROT_NHRP = 0xbf // 191
	// RTPROT_EIGRP      = 0xc0 // 192 // already upstream
	RTPROT_LDP        = 0xc1 // 193
	RTPROT_SHARP      = 0xc2 // 194
	RTPROT_PBR        = 0xc3 // 195
	RTPROT_ZSTATIC    = 0xc4 // 196
	RTPROT_OPENFABRIC = 0xc5 // 197
	RTPROT_SRTE       = 0xc6 // 198
)
