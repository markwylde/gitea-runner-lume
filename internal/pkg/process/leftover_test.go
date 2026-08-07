// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package process

import (
	"path/filepath"
	"testing"
)

func TestPathWithin(t *testing.T) {
	base := filepath.Join("base", "job1")

	cases := []struct {
		name   string
		dir    string
		target string
		fold   bool
		want   bool
	}{
		{"same dir", base, base, false, true},
		{"direct child", base, filepath.Join(base, "app.exe"), false, true},
		{"deep child", base, filepath.Join(base, "sub", "sub", "app.exe"), false, true},
		{"parent is not within child", filepath.Join(base, "sub"), base, false, false},
		{"name-prefix sibling spared", base, filepath.Join("base", "job10", "app.exe"), false, false},
		{"unrelated", base, filepath.Join("other", "app.exe"), false, false},
		{"empty dir", "", base, false, false},
		{"empty target", base, "", false, false},
		{"dot dir", ".", base, false, false},
		{"case-sensitive miss", base, filepath.Join("base", "JOB1", "app.exe"), false, false},
		{"case-insensitive hit", base, filepath.Join("base", "JOB1", "app.exe"), true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pathWithin(filepath.Clean(tc.dir), filepath.Clean(tc.target), tc.fold)
			if got != tc.want {
				t.Fatalf("pathWithin(%q, %q, fold=%v) = %v, want %v", tc.dir, tc.target, tc.fold, got, tc.want)
			}
		})
	}
}
