package frr

import "fmt"

func Chunk[E any](values []E, size int) ([][]E, error) {
	if size <= 0 {
		return nil, fmt.Errorf("size [%d] must be > 0", size)
	}

	var chunks [][]E
	for remaining := len(values); remaining > 0; remaining = len(values) {
		if remaining < size {
			size = remaining
		}

		chunks = append(chunks, values[:size])
		values = values[size:]
	}

	return chunks, nil
}

func DeleteByIndex[T any](slice []T, index int) []T {
	sliceLastIndex := len(slice) - 1
	slice[index] = slice[sliceLastIndex]
	return slice[:sliceLastIndex]
}

func Filter[T any](slice []T, predicate func(T) bool) (res []T) {
	for _, elem := range slice {
		if predicate(elem) {
			res = append(res, elem)
		}
	}
	return res
}

func Exists[T any](slice []T, predicate func(T) bool) (resultIndex int, ok bool) {
	for index, element := range slice {
		if predicate(element) {
			return index, true
		}
	}
	return -1, false
}

func Find[T any](slice []T, predicate func(T) bool) (result T, resultIndex int, ok bool) {
	resultIndex, ok = Exists(slice, predicate)
	if ok {
		result = slice[resultIndex]
	}
	return result, resultIndex, ok
}

func In[Item comparable](items []Item, item Item) bool {
	for i := 0; i < len(items); i++ {
		if items[i] == item {
			return true
		}
	}
	return false
}
