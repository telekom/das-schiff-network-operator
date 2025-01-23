package maps

import (
	"reflect"

	"github.com/google/go-cmp/cmp"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/slice"
)

func AreEqual[M1, M2 ~map[K]V, K comparable, V any](m1 M1, m2 M2) bool {
	if len(m1) != len(m2) {
		return false
	}
	for k, v1 := range m1 {
		if v2, ok := m2[k]; !ok || !cmp.Equal(v1, v2) {
			return false
		}
	}
	return true
}
func Keys[M ~map[K]V, K comparable, V any](data M) []K {
	result := make([]K, 0)
	for key := range data {
		result = append(result, key)
	}
	return result
}
func ForEach[M ~map[K]V, K comparable, V any](elems M, fn func(K, V)) {
	for k, v := range elems {
		fn(k, v)
	}
}
func Deduplicate(elems map[string]interface{}) error {
	for k, v := range elems {
		rVal := reflect.ValueOf(v)
		rType := reflect.TypeOf(v)
		if rType.Kind() == reflect.Map {
			iter := rVal.MapRange()
			subMap := make(map[string]interface{})
			for iter.Next() {
				subMap[iter.Key().String()] = iter.Value().Interface()
			}
			if err := Deduplicate(subMap); err != nil {
				return err
			}
			elems[k] = subMap
		} else if rType.Kind() == reflect.Slice {
			if subSliceAbstract, ok := v.([]interface{}); ok {
				elems[k] = slice.Deduplicate(subSliceAbstract)
			}
		}
	}
	return nil
}

func FromSlice[S any, I comparable](elems []S, key func(S) I) map[I]S {
	result := make(map[I]S)
	for _, item := range elems {
		result[key(item)] = item
	}
	return result
}
