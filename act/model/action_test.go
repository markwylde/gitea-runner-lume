// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package model

import (
	"strings"
	"testing"
)

func TestReadActionDefaultsAndCaseInsensitiveUsing(t *testing.T) {
	action, err := ReadAction(strings.NewReader(`
name: example
runs:
  using: NoDe24
  main: dist/index.js
`))
	if err != nil {
		t.Fatal(err)
	}
	if action.Runs.Using != ActionRunsUsingNode24 {
		t.Fatalf("using = %q, want %q", action.Runs.Using, ActionRunsUsingNode24)
	}
	if action.Runs.PreIf != "always()" {
		t.Fatalf("pre-if = %q, want always()", action.Runs.PreIf)
	}
	if action.Runs.PostIf != "always()" {
		t.Fatalf("post-if = %q, want always()", action.Runs.PostIf)
	}
}

func TestReadActionPreservesExplicitConditions(t *testing.T) {
	action, err := ReadAction(strings.NewReader(`
runs:
  using: composite
  pre-if: success()
  post-if: failure()
  steps:
    - run: echo hello
`))
	if err != nil {
		t.Fatal(err)
	}
	if action.Runs.PreIf != "success()" || action.Runs.PostIf != "failure()" {
		t.Fatalf("conditions = %q/%q, want explicit values", action.Runs.PreIf, action.Runs.PostIf)
	}
	if !action.Runs.Using.IsComposite() || action.Runs.Using.IsDocker() || action.Runs.Using.IsNode() {
		t.Fatalf("unexpected using predicates for %q", action.Runs.Using)
	}
}

func TestReadActionRejectsUnknownUsing(t *testing.T) {
	_, err := ReadAction(strings.NewReader(`
runs:
  using: node99
`))
	if err == nil {
		t.Fatal("expected unknown runs.using to fail")
	}
	if !strings.Contains(err.Error(), "node99") {
		t.Fatalf("error = %q, want invalid value", err)
	}
}

func TestReadActionDockerEntrypoints(t *testing.T) {
	action, err := ReadAction(strings.NewReader(`
runs:
  using: docker
  image: Dockerfile
  pre-entrypoint: pre.sh
  post-entrypoint: post.sh
`))
	if err != nil {
		t.Fatal(err)
	}
	if action.Runs.PreEntrypoint != "pre.sh" {
		t.Fatalf("pre-entrypoint = %q, want pre.sh", action.Runs.PreEntrypoint)
	}
	if action.Runs.PostEntrypoint != "post.sh" {
		t.Fatalf("post-entrypoint = %q, want post.sh", action.Runs.PostEntrypoint)
	}
}
