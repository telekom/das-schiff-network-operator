package maps

import (
	stdmaps "maps"
	"reflect"

	"github.com/google/go-cmp/cmp"

	"github.com/telekom/das-schiff-network-operator/pkg/helpers/slice"
)

func AreEqual[M1, M2 ~map[K]V, K comparable, V any](m1 M1, m2 M2) bool {
	return stdmaps.EqualFunc(m1, m2, func(a, b V) bool { return cmp.Equal(a, b) })
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
