package frr

func Chunk[E any](values []E, size int) [][]E {
	if size <= 0 {
		panic("size must be > 0")
	}

	var chunks [][]E
	for remaining := len(values); remaining > 0; remaining = len(values) {
		if remaining < size {
			size = remaining
		}

		chunks = append(chunks, values[:size:size])
		values = values[size:]
	}

	return chunks
}

func DeleteByIndex[T any](slice []T, index int) []T {
	sliceLastIndex := len(slice) - 1

	if index != sliceLastIndex {
		slice[index] = slice[sliceLastIndex]
	}

	return slice[:sliceLastIndex]
}

func DeleteElementsByIndices[T any](slice []T, indices []int) []T {
	indicesMap := make(map[int]int)

	for _, index := range indices {
		indicesMap[index] = index
	}

	lastIndex := len(slice) - 1
	backIndex := lastIndex

	for _, index := range indices {
		if index < 0 || index > lastIndex {
			continue
		}

		mappedIndex := indicesMap[index]

		if mappedIndex == -1 {
			continue
		}

		if mappedIndex != backIndex {
			slice[mappedIndex] = slice[backIndex]

			indicesMap[backIndex] = indicesMap[mappedIndex]
		}

		indicesMap[index] = -1

		backIndex--
	}

	return slice[:backIndex+1]
}

func Filter[T any](slice []T, predicate func(T) bool) []T {
	indices := make([]int, 0, len(slice))

	for index, element := range slice {
		if !predicate(element) {
			indices = append(indices, index)
		}
	}

	return DeleteElementsByIndices(slice, indices)
}

func Exists[T any](slice []T, predicate func(T) bool) (resultIndex int, ok bool) {
	ok = false
	for index, element := range slice {
		if predicate(element) {
			ok = true
			resultIndex = index
			break
		}
	}
	return resultIndex, ok
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
