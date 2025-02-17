package slice

import (
	"reflect"
	"testing"
)

// TestContainsString calls ContainsString with various inputs.
func TestContainsString(t *testing.T) {
	var stringSlice = []string{"hello", "my", "name", "is", "Joe", "H!4#fawP_4-?", ""}

	// Test existing string
	if !ContainsString(stringSlice, "name") {
		t.Fatalf("string '%s' exists in slice %v", "name", stringSlice)
	}
	// Test existing string with wrong case
	if ContainsString(stringSlice, "joe") {
		t.Fatalf("string '%s' doesn't exist in slice %v", "joe", stringSlice)
	}
	// Test existing string with special characters
	if !ContainsString(stringSlice, "H!4#fawP_4-?") {
		t.Fatalf("string '%s' exists in slice %v", "H!4#fawP_4-?", stringSlice)
	}
	// Test non-existing string
	if ContainsString(stringSlice, "Charlie") {
		t.Fatalf("string '%s' doesn't exist in slice %v", "Charlie", stringSlice)
	}
	// Test empty string
	if !ContainsString(stringSlice, "") {
		t.Fatalf("empty string '%s' exists in slice %v", "", stringSlice)
	}

	// Test uninitialized slice
	var stringSliceUnitialized []string
	if ContainsString(stringSliceUnitialized, "Hello") {
		t.Fatalf("string array is empty and doesn't contain string '%s'", "Hello")
	}
}

// TestRemoveString calls RemoveString with various inputs.
func TestRemoveString(t *testing.T) {
	var stringSlice = []string{"hello", "my", "name", "is", "Joe", "H!4#fawP_4-?", ""}

	// Test existing string
	var stringSliceWant = []string{"hello", "my", "is", "Joe", "H!4#fawP_4-?", ""}
	stringSliceOut := RemoveString(stringSlice, "name")
	if !reflect.DeepEqual(stringSliceOut, stringSliceWant) {
		t.Fatalf("string '%s' was not removed properly, result slice is %v", "name", stringSliceOut)
	}

	// Test existing string with wrong case
	stringSliceWant = []string{"hello", "my", "name", "is", "Joe", "H!4#fawP_4-?", ""}
	stringSliceOut = RemoveString(stringSlice, "joe")
	if !reflect.DeepEqual(stringSliceOut, stringSliceWant) {
		t.Fatalf("string '%s' shouldn't be removed, result slice is %v", "joe", stringSliceOut)
	}

	// Test existing string with special characters
	stringSliceWant = []string{"hello", "my", "name", "is", "Joe", ""}
	stringSliceOut = RemoveString(stringSlice, "H!4#fawP_4-?")
	if !reflect.DeepEqual(stringSliceOut, stringSliceWant) {
		t.Fatalf("string '%s' was not removed properly, result slice is %v", "H!4#fawP_4-?", stringSliceOut)
	}

	// Test non-existing string
	stringSliceWant = []string{"hello", "my", "name", "is", "Joe", "H!4#fawP_4-?", ""}
	stringSliceOut = RemoveString(stringSlice, "Charlie")
	if !reflect.DeepEqual(stringSliceOut, stringSliceWant) {
		t.Fatalf("slices should be equal but result slice is %v", stringSliceOut)
	}

	// Test empty string
	stringSliceWant = []string{"hello", "my", "name", "is", "Joe", "H!4#fawP_4-?"}
	stringSliceOut = RemoveString(stringSlice, "")
	if !reflect.DeepEqual(stringSliceOut, stringSliceWant) {
		t.Fatalf("slices should be equal but result slice is %v", stringSliceOut)
	}

	// Test uninitialized slice
	var stringSliceUnitialized []string
	stringSliceOut = RemoveString(stringSliceUnitialized, "Hello")
	if !reflect.DeepEqual(stringSliceOut, stringSliceUnitialized) {
		t.Fatalf("string array is empty and doesn't contain string '%s'", "Hello")
	}
}
