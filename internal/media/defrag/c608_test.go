package fmp4unfrag

import "testing"

func TestParseC608Sample(t *testing.T) {
	if pairs, ok := parseC608Sample(make([]byte, 12)); !ok || len(pairs) != 0 {
		t.Fatalf("all-zero sample = %v, %v; want empty, ok", pairs, ok)
	}

	sample := []byte{0, 0, 0, 12, 'c', 'd', 'a', 't', 0x94, 0x20, 0x94, 0x2f}
	pairs, ok := parseC608Sample(sample)
	if !ok || len(pairs) != 2 || pairs[1] != [2]byte{0x94, 0x2f} {
		t.Fatalf("cdat sample = %v, %v", pairs, ok)
	}

	if _, ok := parseC608Sample([]byte{0, 0, 0, 12, 'c', 'd', 't', '2', 1, 2, 3, 4}); ok {
		t.Fatal("cdt2 sample must be rejected")
	}
}

// The numbers come from a real Apple Music video (24 fps, 19200 video
// timescale, 1000 caption timescale): an empty lead-in sample followed by an
// 18-pair caption sample whose EOC is pair 16. The caption must appear
// 16/24 s after its sample start.
func TestLayoutC608PairsPacesPairsAtVideoFrameRate(t *testing.T) {
	caption := make([][2]byte, 18)
	caption[16] = [2]byte{0x94, 0x2f}

	times, pairs, end := layoutC608Pairs(
		[][][2]byte{nil, caption},
		[]uint32{1628, 3839},
		1000, 19200, 800,
	)

	if len(times) != 19 {
		t.Fatalf("got %d pairs, want 19", len(times))
	}
	if times[0] != 0 || times[1] != 1628 {
		t.Fatalf("lead-in/caption start = %d/%d, want 0/1628", times[0], times[1])
	}
	const eoc = 1 + 16
	if pairs[eoc] != [2]byte{0x94, 0x2f} || times[eoc] != 2295 {
		t.Fatalf("EOC at %d (%v), want 2295 ms", times[eoc], pairs[eoc])
	}
	if end != 1628+3839 {
		t.Fatalf("end = %d, want %d", end, 1628+3839)
	}
	for k := 1; k < len(times); k++ {
		if times[k] <= times[k-1] {
			t.Fatalf("times not strictly increasing at %d", k)
		}
	}
}

func TestLayoutC608PairsQueuesOverlappingCaptions(t *testing.T) {
	// Two 48-pair captions 1 s apart at 24 fps: the second cannot start before
	// the first one's pairs are consumed (2 s).
	pair := func(n int) [][2]byte { return make([][2]byte, n) }
	times, _, _ := layoutC608Pairs(
		[][][2]byte{pair(48), pair(48)},
		[]uint32{1000, 1000},
		1000, 19200, 800,
	)
	if times[48] != 2000 {
		t.Fatalf("second caption starts at %d, want 2000", times[48])
	}
}
