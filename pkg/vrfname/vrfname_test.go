package vrfname

import "testing"

func TestReduce_ShortNamesUnchanged(t *testing.T) {
	// Anything already within MaxLen must pass through untouched.
	cases := []string{
		"",
		"a",
		"widget",
		"sandbox",
		"green_field",
		"aaa",
		"node12_alpha",    // 12
		"node12_alpha_x",  // 14
		"node12_alphaxy",  // 14
		"fifteencharsxx",  // 14
		"fifteencharsxxx", // 15 exactly
	}
	for _, c := range cases {
		if len(c) > MaxLen {
			t.Fatalf("test bug: case %q is longer than MaxLen", c)
		}
		if got := Reduce(c); got != c {
			t.Errorf("Reduce(%q) = %q, want unchanged", c, got)
		}
	}
}

func TestRemoveInnerVowels(t *testing.T) {
	cases := map[string]string{
		"widget":  "wdgt",  // i between w,d and e between g,t removed
		"sandbox": "sndbx", // a between s,n and o between b,x removed
		"abc":     "abc",   // leading vowel kept
		"cba":     "cba",   // trailing vowel kept
		"c2c":     "c2c",   // vowel? none; unchanged
		"m2z":     "m2z",   // no vowels
		"xax":     "xx",    // a between x,x removed
		"xaax":    "xaax",  // double vowel: each has a vowel neighbour, both kept
		"x_a_x":   "x_a_x", // vowel flanked by underscores kept
		"ax2":     "ax2",   // 'a' leading kept, 'x'? consonant; nothing removed... a is leading
	}
	for in, want := range cases {
		if got := removeInnerVowels(in); got != want {
			t.Errorf("removeInnerVowels(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReduce_VowelRuleFits(t *testing.T) {
	// 16 chars, reduces below MaxLen purely via vowel removal.
	in := "datawarehouses_x"
	got := Reduce(in)
	if len(got) > MaxLen {
		t.Fatalf("Reduce(%q) = %q (len %d) exceeds MaxLen %d", in, got, len(got), MaxLen)
	}
	if !CanReduce(in) {
		t.Errorf("CanReduce(%q) = false, want true", in)
	}
}

func TestReduce_UnderscoreDropRightToLeft(t *testing.T) {
	// No vowels to remove, but dropping the rightmost underscore makes it fit
	// while preserving the leftmost separator.
	in := "wwww_vvvv_bbbb_c"  // 16, no vowels
	want := "wwww_vvvv_bbbbc" // 15, rightmost '_' dropped
	got := Reduce(in)
	if got != want {
		t.Errorf("Reduce(%q) = %q, want %q", in, got, want)
	}
	if len(got) > MaxLen {
		t.Errorf("Reduce(%q) len %d exceeds MaxLen", in, len(got))
	}
}

func TestReduce_Infeasible(t *testing.T) {
	// A long incompressible run (no removable vowels, not enough underscores).
	in := "xxxxxxxx_yyyyyyyy" // 17, one underscore -> 16 after drop, still too long
	if CanReduce(in) {
		t.Errorf("CanReduce(%q) = true, want false", in)
	}
	got := Reduce(in)
	if len(got) <= MaxLen {
		t.Errorf("Reduce(%q) = %q unexpectedly fits", in, got)
	}
}

func TestReduce_Deterministic(t *testing.T) {
	in := "microservice_gateway_frontend_service"
	first := Reduce(in)
	for i := 0; i < 5; i++ {
		if got := Reduce(in); got != first {
			t.Fatalf("Reduce not deterministic: %q vs %q", got, first)
		}
	}
	// Idempotence: reducing a reduced name that fits returns it unchanged.
	if len(first) <= MaxLen {
		if got := Reduce(first); got != first {
			t.Errorf("Reduce not idempotent: Reduce(%q) = %q", first, got)
		}
	}
}

func TestReduce_UniquenessOnSyntheticSet(t *testing.T) {
	// A batch of distinct synthetic names, several longer than MaxLen, must all
	// reduce to distinct results (no collisions) and all fit.
	names := []string{
		"frontend",
		"backend_service",
		"payment_gateway_worker",
		"reporting_pipeline_batch",
		"notification_dispatch",
		"customer_profile_store",
		"widget_zone_seven",
		"tenant_blue_green",
	}
	seen := map[string]string{}
	for _, n := range names {
		r := Reduce(n)
		if !CanReduce(n) {
			t.Errorf("CanReduce(%q) = false", n)
			continue
		}
		if len(r) > MaxLen {
			t.Errorf("Reduce(%q) = %q exceeds MaxLen", n, r)
		}
		if prev, ok := seen[r]; ok {
			t.Errorf("collision: %q and %q both reduce to %q", prev, n, r)
		}
		seen[r] = n
	}
}
