package torrent

import (
	"net"
	"reflect"
	"testing"
)

// Canonical example from BEP 6: ip=80.4.4.200, infohash=0xAA*20,
// numPieces=1313, k=7.
func TestGenerateFastSetBEP6Example(t *testing.T) {
	var ih [20]byte
	for i := range ih {
		ih[i] = 0xAA
	}
	want := []uint32{1059, 431, 808, 1217, 287, 376, 1188}
	got := generateFastSet(7, 1313, ih, net.IPv4(80, 4, 4, 200))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BEP 6 example mismatch:\nwant %v\n got %v", want, got)
	}
}

func TestGenerateFastSetSubnet24(t *testing.T) {
	var ih [20]byte
	for i := range ih {
		ih[i] = byte(i ^ 0x55)
	}
	a := generateFastSet(8, 200, ih, net.IPv4(10, 1, 2, 3))
	b := generateFastSet(8, 200, ih, net.IPv4(10, 1, 2, 250))
	c := generateFastSet(8, 200, ih, net.IPv4(10, 1, 3, 3))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("same /24 gave different sets: %v vs %v", a, b)
	}
	if reflect.DeepEqual(a, c) {
		t.Fatal("different /24 gave the same set")
	}
}

func TestGenerateFastSetEdgeCases(t *testing.T) {
	var ih [20]byte
	if generateFastSet(10, 1000, ih, net.ParseIP("2001:db8::1")) != nil {
		t.Fatal("IPv6 should yield nil")
	}
	if generateFastSet(0, 100, ih, net.IPv4(1, 2, 3, 4)) != nil {
		t.Fatal("k=0 should yield nil")
	}
	if generateFastSet(5, 0, ih, net.IPv4(1, 2, 3, 4)) != nil {
		t.Fatal("numPieces=0 should yield nil")
	}
	got := generateFastSet(7, 7, ih, net.IPv4(203, 0, 113, 1))
	seen := map[uint32]bool{}
	for _, v := range got {
		if seen[v] || v >= 7 {
			t.Fatalf("bad/duplicate index %d in %v", v, got)
		}
		seen[v] = true
	}
}
