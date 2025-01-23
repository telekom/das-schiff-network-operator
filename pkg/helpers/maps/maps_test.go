package maps

import (
	"testing"
)

// TestAreEqual calls AreEqual with various inputs
func TestAreEqual(t *testing.T) {
	map1 := map[string]string{"abc": "def"}

	// Test equal maps
	map2 := map[string]string{"abc": "def"}
	if !AreEqual(map1, map2) {
		t.Fatalf("maps %v and %v are equal", map1, map2)
	}

	// Test different length maps
	map2 = map[string]string{"abc": "def", "hi": "goodbye"}
	if AreEqual(map1, map2) {
		t.Fatalf("maps %v and %v have different length", map1, map2)
	}

	// Test different values
	map2 = map[string]string{"abc": "defg"}
	if AreEqual(map1, map2) {
		t.Fatalf("maps %v and %v are not equal", map1, map2)
	}

	// Test different keys
	map2 = map[string]string{"abc_": "def"}
	if AreEqual(map1, map2) {
		t.Fatalf("maps %v and %v are not equal", map1, map2)
	}

	// Compare to nil
	map2 = nil
	if AreEqual(map1, map2) {
		t.Fatalf("maps %v and %v are not equal", map1, map2)
	}

	// Compare two nil maps
	var map3 map[string]string = nil
	map2 = nil
	if !AreEqual(map3, map2) {
		t.Fatalf("maps %v and %v are both nil and should be equal", map1, map2)
	}

	// Tests with int and bool
	map4 := map[int]bool{2: true, 65: false}
	map5 := map[int]bool{2123123123: true, 65: false}
	if AreEqual(map4, map5) {
		t.Fatalf("maps %v and %v are not equal", map1, map2)
	}
	delete(map5, 2123123123)
	map5[2] = true
	if !AreEqual(map4, map5) {
		t.Fatalf("maps %v and %v are equal", map1, map2)
	}
}
