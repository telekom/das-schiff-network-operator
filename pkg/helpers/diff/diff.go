package diff

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v2"

	"github.com/telekom/das-schiff-network-operator/pkg/helpers/merge"
)

type (
	// YAML has three fundamental types. When unmarshaled into interface{},
	// they're represented like this.
	mapping  = map[interface{}]interface{}
	sequence = []interface{}
)

// YAML deep-merges any number of YAML sources, with later sources taking
// priority over earlier ones.
//
// Maps are deep-merged. For example,
//
//	{"one": 1, "two": 2} + {"one": 42, "three": 3}
//	== {"one": 42, "two": 2, "three": 3}
//
// Sequences are replaced. For example,
//
//	{"foo": [1, 2, 3]} + {"foo": [4, 5, 6]}
//	== {"foo": [4, 5, 6]}
//
// In non-strict mode, duplicate map keys are allowed within a single source,
// with later values overwriting previous ones. Attempting to merge
// mismatched types (e.g., merging a sequence into a map) replaces the old
// value with the new.
//
// Enabling strict mode returns errors in both of the above cases.
func FindYAMLOverrides(origin, final []byte) (*bytes.Buffer, error) {
	originDecoder := yaml.NewDecoder(bytes.NewReader(origin))
	var originContents interface{}
	if err := originDecoder.Decode(&originContents); errors.Is(err, io.EOF) {
		// Skip empty and comment-only sources, which we should handle
		// differently from explicit nils.
		return nil, fmt.Errorf("couldn't decode source: %w", err)
	} else if err != nil {
		return nil, fmt.Errorf("couldn't decode source: %w", err)
	}
	finalDecoder := yaml.NewDecoder(bytes.NewReader(final))
	var finalContents interface{}
	if err := finalDecoder.Decode(&finalContents); errors.Is(err, io.EOF) {
		// Skip empty and comment-only sources, which we should handle
		// differently from explicit nils.
		return nil, fmt.Errorf("couldn't decode source: %w", err)
	} else if err != nil {
		return nil, fmt.Errorf("couldn't decode source: %w", err)
	}

	buf := &bytes.Buffer{}
	if result, err := findOverrides(originContents, finalContents); err != nil {
		return nil, err
	} else if result != nil {
		enc := yaml.NewEncoder(buf)
		if err := enc.Encode(result); err != nil {
			return nil, fmt.Errorf("couldn't re-serialize final YAML: %w", err)
		}
	}
	return buf, nil
}

func findOverrides(origin, final interface{}) (interface{}, error) {
	// It's possible to handle this with a mass of reflection, but we only need
	// to merge whole YAML files. Since we're always unmarshaling into
	// interface{}, we only need to handle a few types. This ends up being
	// cleaner if we just handle each case explicitly.
	if final == nil {
		return origin, nil
	}
	if origin == nil {
		// Allow higher-priority YAML to explicitly nil out lower-priority entries.
		return nil, nil
	}
	if merge.IsScalar(final) && merge.IsScalar(origin) {
		if !cmp.Equal(final, origin) {
			return final, nil
		}
		return nil, nil
	}
	if merge.IsSequence(final) && merge.IsSequence(origin) {
		return findOverridesSequence(origin.(sequence), final.(sequence))
	}
	if merge.IsMapping(final) && merge.IsMapping(origin) {
		return findOverridesMapping(origin.(mapping), final.(mapping))
	}
	return final, nil
}
func findOverridesSequence(from, into sequence) (interface{}, error) {
	result := make(sequence, 0)
	for _, item := range from {
		if !slices.ContainsFunc(into, func(candidate interface{}) bool { return cmp.Equal(item, candidate) }) {
			result = append(result, item)
		}
	}
	if len(result) > 0 {
		return result, nil
	}
	return nil, nil
}
func findOverridesMapping(from, into mapping) (interface{}, error) {
	result := make(mapping)
	for k, v := range from {
		if replace, ok := into[k]; ok {
			if override, err := findOverrides(v, replace); err != nil {
				return nil, err
			} else if override != nil {
				result[k] = override
			}
		}
	}
	if len(result) > 0 {
		return result, nil
	}
	return nil, nil
}
