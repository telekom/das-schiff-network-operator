// Package vrfname implements the deterministic reduction of human-readable VRF
// names into names that fit within the Linux interface-name limit.
//
// The datapath uses a VRF name directly as the name of the VRF netlink device,
// which is bound by IFNAMSIZ (15 usable bytes). Bridge/VXLAN interfaces are no
// longer derived from the VRF name (they use the VNI) and the SBR intermediate
// VRF uses a hash, so the VRF device name itself is the only remaining
// constraint. This package reduces a (possibly longer) readable name to at most
// MaxLen bytes using a stable, collision-resistant cascade so the same input
// always yields the same output.
//
// The cascade is:
//  1. return the name unchanged if it already fits;
//  2. otherwise remove vowels that sit between two "hard" characters
//     (a consonant or a digit) — boundaries and '_' block removal, so leading
//     and trailing vowels of a segment are preserved;
//  3. if it still does not fit, drop '_' separators from right to left until it
//     fits (this preserves a leading tenant-style prefix the longest).
//
// The package performs no truncation: if a name cannot be reduced to MaxLen
// (for example a single long run of consonants/digits), Reduce returns the
// best-effort result and CanReduce reports false so a webhook can reject it.
package vrfname

import (
	"crypto/sha256"
	"encoding/hex"
)

// MaxLen is the maximum length of a reduced VRF name. It equals the usable
// Linux interface-name length (IFNAMSIZ - 1).
const MaxLen = 15

// sbrPrefix prefixes the name of an SBR (source-based routing) intermediate
// VRF. The name is always derived from a hash so that it is a fixed, short
// length regardless of the (possibly long) source key.
const sbrPrefix = "s-"

// SBRName returns the name of the SBR intermediate VRF for the given key. The
// key is hashed so the result is always sbrPrefix + 8 hex chars = 10 bytes,
// which fits within MaxLen for any input. It is deterministic: the same key
// always yields the same name.
func SBRName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return sbrPrefix + hex.EncodeToString(sum[:])[:8]
}

// Reduce shortens name to at most MaxLen bytes using the cascade described in
// the package documentation. If the name cannot be reduced to fit, the
// best-effort reduced form is returned (which may still exceed MaxLen); callers
// that need to guarantee a fit should use CanReduce.
func Reduce(name string) string {
	if len(name) <= MaxLen {
		return name
	}

	reduced := removeInnerVowels(name)
	if len(reduced) <= MaxLen {
		return reduced
	}

	return dropUnderscoresRTL(reduced, MaxLen)
}

// CanReduce reports whether name can be reduced to at most MaxLen bytes.
func CanReduce(name string) bool {
	return len(Reduce(name)) <= MaxLen
}

// isVowel reports whether r is an ASCII vowel (case-insensitive).
func isVowel(r byte) bool {
	switch r {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return true
	default:
		return false
	}
}

// isHard reports whether r is a "hard" character: a consonant or a digit.
// Underscores, other separators and vowels are soft and therefore block the
// removal of an adjacent vowel.
func isHard(r byte) bool {
	switch {
	case r >= '0' && r <= '9':
		return true
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return !isVowel(r)
	default:
		return false
	}
}

// removeInnerVowels drops every vowel whose neighbours (in the original string)
// are both hard characters. Neighbours are evaluated against the original
// string so removals do not cascade.
func removeInnerVowels(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isVowel(c) && i > 0 && i < len(s)-1 && isHard(s[i-1]) && isHard(s[i+1]) {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// dropUnderscoresRTL removes '_' characters from right to left until the string
// fits within target bytes or no underscores remain.
func dropUnderscoresRTL(s string, target int) string {
	for len(s) > target {
		idx := lastIndexByte(s, '_')
		if idx < 0 {
			break
		}
		s = s[:idx] + s[idx+1:]
	}
	return s
}

// lastIndexByte returns the index of the last occurrence of b in s, or -1.
func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}
