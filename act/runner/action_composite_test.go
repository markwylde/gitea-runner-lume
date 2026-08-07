// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAppendUniqueMasks(t *testing.T) {
	tests := []struct {
		name string
		dst  []string
		src  []string
		want []string
	}{
		{
			name: "appends new masks",
			dst:  []string{"a"},
			src:  []string{"b", "c"},
			want: []string{"a", "b", "c"},
		},
		{
			name: "skips masks already present",
			dst:  []string{"a", "b"},
			src:  []string{"a", "b"},
			want: []string{"a", "b"},
		},
		{
			name: "deduplicates within src",
			dst:  []string{"a"},
			src:  []string{"b", "b", "a"},
			want: []string{"a", "b"},
		},
		{
			name: "empty src leaves dst unchanged",
			dst:  []string{"a"},
			src:  nil,
			want: []string{"a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, appendUniqueMasks(tt.dst, tt.src))
		})
	}
}

// TestAppendUniqueMasksNoExponentialGrowth reproduces the exponential growth of
// the parent's Masks slice observed with nested/repeated composite actions. A
// composite RunContext is seeded with its parent's masks and the whole seeded
// slice was previously appended back into the parent, doubling its length on
// every composite action.
func TestAppendUniqueMasksNoExponentialGrowth(t *testing.T) {
	parentMasks := []string{"secret"}

	for range 20 {
		// compositeRC.Masks starts as a copy of the parent's masks (it is
		// seeded with parent.Masks in newCompositeRunContext).
		compositeMasks := make([]string, len(parentMasks))
		copy(compositeMasks, parentMasks)

		parentMasks = appendUniqueMasks(parentMasks, compositeMasks)
	}

	assert.Equal(t, []string{"secret"}, parentMasks)
}
