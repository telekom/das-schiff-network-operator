package slice

import (
	"github.com/cnf/structhash"
)

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
