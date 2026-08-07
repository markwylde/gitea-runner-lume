// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2021 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package model

import (
	"path/filepath"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type WorkflowPlanTest struct {
	workflowPath      string
	errorMessage      string
	noWorkflowRecurse bool
}

func TestPlanner(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	tables := []WorkflowPlanTest{
		{"invalid-job-name/invalid-1.yml", "workflow is not valid. 'invalid-job-name-1': Job name 'invalid-JOB-Name-v1.2.3-docker_hub' is invalid. Names must start with a letter or '_' and contain only alphanumeric characters, '-', or '_'", false},
		{"invalid-job-name/invalid-2.yml", "workflow is not valid. 'invalid-job-name-2': Job name '1234invalid-JOB-Name-v123-docker_hub' is invalid. Names must start with a letter or '_' and contain only alphanumeric characters, '-', or '_'", false},
		{"invalid-job-name/valid-1.yml", "", false},
		{"invalid-job-name/valid-2.yml", "", false},
		{"empty-workflow", "unable to read workflow 'push.yml': file is empty: EOF", false},
		{"nested", "unable to read workflow 'fail.yml': file is empty: EOF", false},
		{"nested", "", true},
	}

	workdir, err := filepath.Abs("testdata")
	assert.NoError(t, err, workdir) //nolint:testifylint // pre-existing issue from nektos/act
	for _, table := range tables {
		fullWorkflowPath := filepath.Join(workdir, table.workflowPath)
		_, err = NewWorkflowPlanner(fullWorkflowPath, table.noWorkflowRecurse)
		if table.errorMessage == "" {
			assert.NoError(t, err, "WorkflowPlanner should exit without any error")
		} else {
			assert.EqualError(t, err, table.errorMessage)
		}
	}
}

func TestWorkflow(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	workflow := Workflow{
		Jobs: map[string]*Job{
			"valid_job": {
				Name: "valid_job",
			},
		},
	}

	// Check that an invalid job id returns error
	result, err := createStages(&workflow, "invalid_job_id")
	assert.Error(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Nil(t, result)

	// Check that an valid job id returns non-error
	result, err = createStages(&workflow, "valid_job")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.NotNil(t, result)
}

func TestNewSingleWorkflowPlannerAndPlanMethods(t *testing.T) {
	planner, err := NewSingleWorkflowPlanner("ci.yml", strings.NewReader(`
name: CI
on: [push, pull_request]
jobs:
  build:
    name: Build project
    runs-on: ubuntu-latest
    steps:
      - run: make build
  test:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - run: make test
`))
	require.NoError(t, err)

	assert.Equal(t, []string{"pull_request", "push"}, planner.GetEvents())

	eventPlan, err := planner.PlanEvent("push")
	require.NoError(t, err)
	require.Len(t, eventPlan.Stages, 2)
	assert.Equal(t, []string{"build"}, eventPlan.Stages[0].GetJobIDs())
	assert.Equal(t, []string{"test"}, eventPlan.Stages[1].GetJobIDs())
	assert.Equal(t, len("Build project"), eventPlan.MaxRunNameLen())
	assert.Equal(t, "Build project", eventPlan.Stages[0].Runs[0].String())
	assert.Equal(t, "build", eventPlan.Stages[0].Runs[0].JobID)
	assert.NotNil(t, eventPlan.Stages[0].Runs[0].Job())

	jobPlan, err := planner.PlanJob("test")
	require.NoError(t, err)
	require.Len(t, jobPlan.Stages, 2)
	assert.Equal(t, []string{"build"}, jobPlan.Stages[0].GetJobIDs())
	assert.Equal(t, []string{"test"}, jobPlan.Stages[1].GetJobIDs())

	allPlan, err := planner.PlanAll()
	require.NoError(t, err)
	require.Len(t, allPlan.Stages, 2)
	assert.Equal(t, []string{"build"}, allPlan.Stages[0].GetJobIDs())
	assert.Equal(t, []string{"test"}, allPlan.Stages[1].GetJobIDs())
}

func TestCombineWorkflowPlannerMergesWorkflowStages(t *testing.T) {
	first := mustReadWorkflow(t, `
name: First
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: make build
`)
	second := mustReadWorkflow(t, `
name: Second
on: push
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - run: make lint
  test:
    needs: lint
    runs-on: ubuntu-latest
    steps:
      - run: make test
`)

	planner := CombineWorkflowPlanner(first, second)
	plan, err := planner.PlanEvent("push")
	require.NoError(t, err)
	require.Len(t, plan.Stages, 2)
	assert.ElementsMatch(t, []string{"build", "lint"}, plan.Stages[0].GetJobIDs())
	assert.Equal(t, []string{"test"}, plan.Stages[1].GetJobIDs())

	empty, err := planner.PlanEvent("schedule")
	require.NoError(t, err)
	assert.Empty(t, empty.Stages)
}

func TestPlannerErrorsForMissingAndCyclicJobs(t *testing.T) {
	workflow := mustReadWorkflow(t, `
name: Cyclic
on: push
jobs:
  a:
    needs: b
    runs-on: ubuntu-latest
    steps:
      - run: echo a
  b:
    needs: a
    runs-on: ubuntu-latest
    steps:
      - run: echo b
`)
	planner := CombineWorkflowPlanner(workflow)

	plan, err := planner.PlanJob("missing")
	require.Error(t, err)
	assert.Empty(t, plan.Stages)
	assert.Contains(t, err.Error(), "Could not find any stages")

	plan, err = planner.PlanEvent("push")
	require.Error(t, err)
	assert.Empty(t, plan.Stages)
	assert.Contains(t, err.Error(), "unable to build dependency graph")
}

func TestNewSingleWorkflowPlannerErrors(t *testing.T) {
	_, err := NewSingleWorkflowPlanner("empty.yml", strings.NewReader(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file is empty")

	_, err = NewSingleWorkflowPlanner("invalid.yml", strings.NewReader("jobs: ["))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow is not valid")
}

func mustReadWorkflow(t *testing.T, content string) *Workflow {
	t.Helper()

	workflow, err := ReadWorkflow(strings.NewReader(content))
	require.NoError(t, err)
	if workflow.Name == "" {
		workflow.Name = "workflow"
	}
	return workflow
}
