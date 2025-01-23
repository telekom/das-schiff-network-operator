package slice

import (
	"github.com/cnf/structhash"
	"github.com/google/go-cmp/cmp"
)

// ContainsString tests if list of strings contains one specific string `s`
func ContainsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// RemoveString from slice
func RemoveString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}

// IsEquivalent returns true if two string slices contain the same items agnostic to item order
func IsEquivalent(a *[]string, b *[]string) bool {
	if len(*a) != len(*b) {
		return false
	}
	for _, aValue := range *a {
		found := false
		for _, bValue := range *b {
			if aValue == bValue {
				found = true
				break
			}
		}
		// if did not find at least one finish
		if !found {
			return false
		}
	}
	return true
}

// Contains returns true if a slice contains an element
func Contains[T any](elems []T, v T) bool {
	return IndexOf(elems, v) >= 0
}

// Intersects returns true if two slices have at least 1 common element
func Intersects[T comparable](s1 []T, s2 []T) bool {
	for _, item := range s1 {
		if Contains(s2, item) {
			return true
		}
	}
	return false
}

// IndexOf returns the index of an element in a slice, if exists (otherwise -1)
func IndexOf[T any](elems []T, v T) int {
	for i, s := range elems {
		if cmp.Equal(v, s) {
			return i
		}
	}
	return -1
}

func Map[T any, R any](elems []T, fn func(T) R) []R {
	result := make([]R, len(elems))
	for i, e := range elems {
		result[i] = fn(e)
	}
	return result
}

func ForEach[T any](elems []T, fn func(T)) {
	for _, e := range elems {
		fn(e)
	}
}

// Remove removes an element of a generic slice
func Remove[T comparable](elems []T, v T) []T {
	if index := IndexOf(elems, v); index >= 0 {
		return append(elems[:index], elems[index+1:]...)
	}
	return elems
}
func Deduplicate[T any](elems []T) []T {
	resultMap := make(map[string]struct{})
	result := make([]T, 0)
	for _, item := range elems {
		key := string(structhash.Sha1(item, 1))
		if _, exists := resultMap[key]; !exists {
			resultMap[key] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

func Find[T any](input []T, finder func(T, int) bool) *T {
	for index, item := range input {
		if finder(item, index) {
			return &item
		}
	}
	return nil
}
