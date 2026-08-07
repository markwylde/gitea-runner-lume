// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
)

// TestStepCloneIsolatesMutableFields guards the parallel-matrix race fix: combinations share the
// job's *Step, and Clone() must hand each a copy whose If/Env nodes and With map can be mutated
// independently. A shallow copy would share Env.Content's backing array (and the With map) and
// leak writes across combinations.
func TestStepCloneIsolatesMutableFields(t *testing.T) {
	var orig Step
	require.NoError(t, yaml.Unmarshal([]byte("if: ${{ env.X == 'a' }}\nenv:\n  KEY: original\nwith:\n  arg: original\n"), &orig))
	require.Len(t, orig.Env.Content, 2) // [key, value]

	clone := orig.Clone()
	clone.If.Value = "changed"
	clone.Env.Content[1].Value = "changed"
	clone.With["arg"] = "changed"

	assert.Equal(t, "${{ env.X == 'a' }}", orig.If.Value, "If must not be shared with the clone")
	assert.Equal(t, "original", orig.Env.Content[1].Value, "Env nodes must not be shared with the clone")
	assert.Equal(t, "original", orig.With["arg"], "With map must not be shared with the clone")
}

// TestJobNeedsResult guards the continue-on-error semantics exposed to dependent
// jobs through the `needs` context: a failed-but-tolerated job reports "success"
// so it does not block dependents gated on the default `if: success()`, matching
// GitHub. A firm failure and any non-failure result are reported verbatim.
func TestJobNeedsResult(t *testing.T) {
	cases := []struct {
		name            string
		result          string
		continueOnError bool
		want            string
	}{
		{"tolerated failure reports success", "failure", true, "success"},
		{"firm failure reports failure", "failure", false, "failure"},
		{"success is unchanged", "success", false, "success"},
		{"success with continue-on-error is unchanged", "success", true, "success"},
		{"empty result is unchanged", "", true, ""},
		{"skipped is unchanged", "skipped", true, "skipped"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &Job{Result: tc.result, ContinueOnError: tc.continueOnError}
			assert.Equal(t, tc.want, j.NeedsResult())
		})
	}
}

func TestJobSetContinueOnErrorFirmFailureWins(t *testing.T) {
	job := &Job{}
	job.SetContinueOnError(true)
	assert.True(t, job.ContinueOnError)

	job.SetContinueOnError(false)
	assert.False(t, job.ContinueOnError)

	job.SetContinueOnError(true)
	assert.False(t, job.ContinueOnError, "a later tolerated failure must not hide an earlier firm failure")
}

func TestStepStatusText(t *testing.T) {
	for _, tc := range []struct {
		status stepStatus
		text   string
	}{
		{StepStatusSuccess, "success"},
		{StepStatusFailure, "failure"},
		{StepStatusSkipped, "skipped"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, err := tc.status.MarshalText()
			require.NoError(t, err)
			assert.Equal(t, tc.text, string(got))

			var parsed stepStatus
			require.NoError(t, parsed.UnmarshalText(got))
			assert.Equal(t, tc.status, parsed)
			assert.Equal(t, tc.text, parsed.String())
		})
	}

	var parsed stepStatus
	require.Error(t, parsed.UnmarshalText([]byte("cancelled")))
	assert.Empty(t, stepStatus(99).String())
}

func TestWorkflowCallConfig(t *testing.T) {
	workflow, err := ReadWorkflow(strings.NewReader(`
on:
  workflow_call:
    inputs:
      name:
        required: true
        type: string
    outputs:
      digest:
        value: ${{ jobs.build.outputs.digest }}
jobs: {}
`))
	require.NoError(t, err)

	config := workflow.WorkflowCallConfig()
	require.NotNil(t, config)
	require.Contains(t, config.Inputs, "name")
	assert.True(t, config.Inputs["name"].Required)
	assert.Equal(t, "string", config.Inputs["name"].Type)
	assert.Equal(t, "${{ jobs.build.outputs.digest }}", config.Outputs["digest"].Value)

	listWorkflow, err := ReadWorkflow(strings.NewReader("on: [workflow_call]\njobs: {}\n"))
	require.NoError(t, err)
	assert.NotNil(t, listWorkflow.WorkflowCallConfig())
	assert.Empty(t, listWorkflow.WorkflowCallConfig().Inputs)
}

func TestJobSecretsAndEnvironment(t *testing.T) {
	inheritJob := readJob(t, `
secrets: inherit
env:
  A: one
  B: two
`)
	assert.True(t, inheritJob.InheritSecrets())
	assert.Nil(t, inheritJob.Secrets())
	assert.Equal(t, map[string]string{"A": "one", "B": "two"}, inheritJob.Environment())

	mappingJob := readJob(t, `
secrets:
  TOKEN: ${{ secrets.TOKEN }}
`)
	assert.False(t, mappingJob.InheritSecrets())
	assert.Equal(t, map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}, mappingJob.Secrets())
}

func TestJobTypeAndString(t *testing.T) {
	tests := []struct {
		job     Job
		want    JobType
		wantErr bool
	}{
		{job: Job{}, want: JobTypeDefault},
		{job: Job{Uses: "./.github/workflows/reuse.yml"}, want: JobTypeReusableWorkflowLocal},
		{job: Job{Uses: "owner/repo/.github/workflows/reuse.yaml@v1"}, want: JobTypeReusableWorkflowRemote},
		{job: Job{Uses: "owner/repo/.github/workflows/reuse.yaml"}, want: JobTypeInvalid, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s/%s", tc.job.Uses, tc.want), func(t *testing.T) {
			got, err := tc.job.Type()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, got)
		})
	}

	assert.Equal(t, "default", JobTypeDefault.String())
	assert.Equal(t, "local-reusable-workflow", JobTypeReusableWorkflowLocal.String())
	assert.Equal(t, "remote-reusable-workflow", JobTypeReusableWorkflowRemote.String())
	assert.Equal(t, "unknown", JobType(99).String())
}

func TestStepStringEnvironmentEnvAndType(t *testing.T) {
	step := readStep(t, `
id: example
env:
  DIRECT: value
with:
  mixed-key: input
`)
	assert.Equal(t, "example", step.String())
	assert.Equal(t, map[string]string{"DIRECT": "value"}, step.Environment())
	assert.Equal(t, map[string]string{"DIRECT": "value", "INPUT_MIXED-KEY": "input"}, step.GetEnv())

	for _, tc := range []struct {
		step Step
		want StepType
	}{
		{step: Step{}, want: StepTypeInvalid},
		{step: Step{Run: "echo hi"}, want: StepTypeRun},
		{step: Step{Run: "echo hi", Uses: "actions/checkout@v4"}, want: StepTypeInvalid},
		{step: Step{Uses: "docker://alpine:latest"}, want: StepTypeUsesDockerURL},
		{step: Step{Uses: "./.github/workflows/reuse.yml"}, want: StepTypeReusableWorkflowLocal},
		{step: Step{Uses: "owner/repo/.github/workflows/reuse.yml@v1"}, want: StepTypeReusableWorkflowRemote},
		{step: Step{Uses: "./actions/local"}, want: StepTypeUsesActionLocal},
		{step: Step{Uses: "actions/checkout@v4"}, want: StepTypeUsesActionRemote},
	} {
		t.Run(tc.want.String(), func(t *testing.T) {
			assert.Equal(t, tc.want, tc.step.Type())
		})
	}

	assert.Equal(t, "invalid", StepTypeInvalid.String())
	assert.Equal(t, "run", StepTypeRun.String())
	assert.Equal(t, "local-action", StepTypeUsesActionLocal.String())
	assert.Equal(t, "remote-action", StepTypeUsesActionRemote.String())
	assert.Equal(t, "docker", StepTypeUsesDockerURL.String())
	assert.Equal(t, "local-reusable-workflow", StepTypeReusableWorkflowLocal.String())
	assert.Equal(t, "remote-reusable-workflow", StepTypeReusableWorkflowRemote.String())
	assert.Equal(t, "unknown", StepType(99).String())
	assert.NotEmpty(t, (&Step{Uses: "actions/checkout@v4"}).UsesHash())
}

func TestWorkflowGetJobAndIDs(t *testing.T) {
	workflow := &Workflow{Jobs: map[string]*Job{"build": {}}}
	assert.Equal(t, []string{"build"}, workflow.GetJobIDs())

	job := workflow.GetJob("build")
	require.NotNil(t, job)
	assert.Equal(t, "build", job.Name)
	assert.Equal(t, "success()", job.If.Value)
	assert.Nil(t, workflow.GetJob("missing"))
}

func TestRawConcurrencyYaml(t *testing.T) {
	var expr RawConcurrency
	require.NoError(t, yaml.Unmarshal([]byte("group-${{ github.ref }}"), &expr))
	assert.Equal(t, "group-${{ github.ref }}", expr.RawExpression)
	marshaled, err := expr.MarshalYAML()
	require.NoError(t, err)
	assert.Equal(t, "group-${{ github.ref }}", marshaled)

	var object RawConcurrency
	require.NoError(t, yaml.Unmarshal([]byte("group: ci\ncancel-in-progress: true\n"), &object))
	assert.Equal(t, "ci", object.Group)
	assert.Equal(t, "true", object.CancelInProgress)
	marshaled, err = object.MarshalYAML()
	require.NoError(t, err)
	assert.Equal(t, (*objectConcurrency)(&object), marshaled)
}

func TestReadWorkflow_ScheduleEvent(t *testing.T) {
	yaml := `
name: local-action-docker-url
on:
  schedule:
    - cron: '30 5 * * 1,3'
    - cron: '30 5 * * 2,4'

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	schedules := workflow.OnEvent("schedule")
	assert.Len(t, schedules, 2)

	newSchedules := workflow.OnSchedule()
	assert.Len(t, newSchedules, 2)

	assert.Equal(t, "30 5 * * 1,3", newSchedules[0])
	assert.Equal(t, "30 5 * * 2,4", newSchedules[1])

	yaml = `
name: local-action-docker-url
on:
  schedule:
    test: '30 5 * * 1,3'

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	newSchedules = workflow.OnSchedule()
	assert.Empty(t, newSchedules)

	yaml = `
name: local-action-docker-url
on:
  schedule:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	newSchedules = workflow.OnSchedule()
	assert.Empty(t, newSchedules)

	yaml = `
name: local-action-docker-url
on: [push, tag]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	newSchedules = workflow.OnSchedule()
	assert.Empty(t, newSchedules)
}

func TestReadWorkflow_StringEvent(t *testing.T) {
	yaml := `
name: local-action-docker-url
on: push

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	assert.Len(t, workflow.On(), 1)
	assert.Contains(t, workflow.On(), "push")
}

func TestReadWorkflow_ListEvent(t *testing.T) {
	yaml := `
name: local-action-docker-url
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	assert.Len(t, workflow.On(), 2)
	assert.Contains(t, workflow.On(), "push")
	assert.Contains(t, workflow.On(), "pull_request")
}

func TestReadWorkflow_MapEvent(t *testing.T) {
	yaml := `
name: local-action-docker-url
on:
  push:
    branches:
    - master
  pull_request:
    branches:
    - master

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.On(), 2)
	assert.Contains(t, workflow.On(), "push")
	assert.Contains(t, workflow.On(), "pull_request")
}

func TestReadWorkflow_RunsOnLabels(t *testing.T) {
	yaml := `
name: local-action-docker-url

jobs:
  test:
    container: nginx:latest
    runs-on:
      labels: ubuntu-latest
    steps:
    - uses: ./actions/docker-url`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed")                     //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].RunsOn(), []string{"ubuntu-latest"}) //nolint:testifylint // pre-existing issue from nektos/act
}

func TestReadWorkflow_RunsOnLabelsWithGroup(t *testing.T) {
	yaml := `
name: local-action-docker-url

jobs:
  test:
    container: nginx:latest
    runs-on:
      labels: [ubuntu-latest]
      group: linux
    steps:
    - uses: ./actions/docker-url`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed")                              //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].RunsOn(), []string{"ubuntu-latest", "linux"}) //nolint:testifylint // pre-existing issue from nektos/act
}

func TestReadWorkflow_StringContainer(t *testing.T) {
	yaml := `
name: local-action-docker-url

jobs:
  test:
    container: nginx:latest
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
  test2:
    container:
      image: nginx:latest
      env:
        foo: bar
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 2)
	assert.Contains(t, workflow.Jobs["test"].Container().Image, "nginx:latest")
	assert.Contains(t, workflow.Jobs["test2"].Container().Image, "nginx:latest")
	assert.Contains(t, workflow.Jobs["test2"].Container().Env["foo"], "bar")
}

func TestReadWorkflow_ObjectContainer(t *testing.T) {
	yaml := `
name: local-action-docker-url

jobs:
  test:
    container:
      image: r.example.org/something:latest
      credentials:
        username: registry-username
        password: registry-password
      env:
        HOME: /home/user
      volumes:
        - my_docker_volume:/volume_mount
        - /data/my_data
        - /source/directory:/destination/directory
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 1)

	container := workflow.GetJob("test").Container()

	assert.Contains(t, container.Image, "r.example.org/something:latest")
	assert.Contains(t, container.Env["HOME"], "/home/user")
	assert.Contains(t, container.Credentials["username"], "registry-username")
	assert.Contains(t, container.Credentials["password"], "registry-password")
	assert.ElementsMatch(t, container.Volumes, []string{
		"my_docker_volume:/volume_mount",
		"/data/my_data",
		"/source/directory:/destination/directory",
	})
}

func TestReadWorkflow_JobTypes(t *testing.T) {
	yaml := `
name: invalid job definition

jobs:
  default-job:
    runs-on: ubuntu-latest
    steps:
      - run: echo
  remote-reusable-workflow-yml:
    uses: remote/repo/some/path/to/workflow.yml@main
  remote-reusable-workflow-yaml:
    uses: remote/repo/some/path/to/workflow.yaml@main
  remote-reusable-workflow-custom-path:
    uses: remote/repo/path/to/workflow.yml@main
  local-reusable-workflow-yml:
    uses: ./some/path/to/workflow.yml
  local-reusable-workflow-yaml:
    uses: ./some/path/to/workflow.yaml
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 6)

	jobType, err := workflow.Jobs["default-job"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeDefault, jobType)

	jobType, err = workflow.Jobs["remote-reusable-workflow-yml"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeReusableWorkflowRemote, jobType)

	jobType, err = workflow.Jobs["remote-reusable-workflow-yaml"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeReusableWorkflowRemote, jobType)

	jobType, err = workflow.Jobs["remote-reusable-workflow-custom-path"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeReusableWorkflowRemote, jobType)

	jobType, err = workflow.Jobs["local-reusable-workflow-yml"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeReusableWorkflowLocal, jobType)

	jobType, err = workflow.Jobs["local-reusable-workflow-yaml"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeReusableWorkflowLocal, jobType)
}

func TestReadWorkflow_JobTypes_InvalidPath(t *testing.T) {
	yaml := `
name: invalid job definition

jobs:
  remote-reusable-workflow-missing-version:
    uses: remote/repo/some/path/to/workflow.yml
  remote-reusable-workflow-bad-extension:
    uses: remote/repo/some/path/to/workflow.json
  local-reusable-workflow-bad-extension:
    uses: ./some/path/to/workflow.json
  local-reusable-workflow-bad-path:
    uses: some/path/to/workflow.yaml
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 4)

	jobType, err := workflow.Jobs["remote-reusable-workflow-missing-version"].Type()
	assert.Equal(t, JobTypeInvalid, jobType)
	assert.NotEqual(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act

	jobType, err = workflow.Jobs["remote-reusable-workflow-bad-extension"].Type()
	assert.Equal(t, JobTypeInvalid, jobType)
	assert.NotEqual(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act

	jobType, err = workflow.Jobs["local-reusable-workflow-bad-extension"].Type()
	assert.Equal(t, JobTypeInvalid, jobType)
	assert.NotEqual(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act

	jobType, err = workflow.Jobs["local-reusable-workflow-bad-path"].Type()
	assert.Equal(t, JobTypeInvalid, jobType)
	assert.NotEqual(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
}

func TestReadWorkflow_StepsTypes(t *testing.T) {
	yaml := `
name: invalid step definition

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: test1
        uses: actions/checkout@v2
        run: echo
      - name: test2
        run: echo
      - name: test3
        uses: actions/checkout@v2
      - name: test4
        uses: docker://nginx:latest
      - name: test5
        uses: ./local-action
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 1)
	assert.Len(t, workflow.Jobs["test"].Steps, 5)
	assert.Equal(t, workflow.Jobs["test"].Steps[0].Type(), StepTypeInvalid)          //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].Steps[1].Type(), StepTypeRun)              //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].Steps[2].Type(), StepTypeUsesActionRemote) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].Steps[3].Type(), StepTypeUsesDockerURL)    //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].Steps[4].Type(), StepTypeUsesActionLocal)  //nolint:testifylint // pre-existing issue from nektos/act
}

// See: https://docs.github.com/en/actions/reference/workflow-syntax-for-github-actions#jobsjob_idoutputs
func TestReadWorkflow_JobOutputs(t *testing.T) {
	yaml := `
name: job outputs definition

jobs:
  test1:
    runs-on: ubuntu-latest
    steps:
      - id: test1_1
        run: |
          echo "::set-output name=a_key::some-a_value"
          echo "::set-output name=b-key::some-b-value"
    outputs:
      some_a_key: ${{ steps.test1_1.outputs.a_key }}
      some-b-key: ${{ steps.test1_1.outputs.b-key }}

  test2:
    runs-on: ubuntu-latest
    needs:
      - test1
    steps:
      - name: test2_1
        run: |
          echo "${{ needs.test1.outputs.some_a_key }}"
          echo "${{ needs.test1.outputs.some-b-key }}"
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 2)

	assert.Len(t, workflow.Jobs["test1"].Steps, 1)
	assert.Equal(t, StepTypeRun, workflow.Jobs["test1"].Steps[0].Type())
	assert.Equal(t, "test1_1", workflow.Jobs["test1"].Steps[0].ID)
	assert.Len(t, workflow.Jobs["test1"].Outputs, 2)
	assert.Contains(t, workflow.Jobs["test1"].Outputs, "some_a_key")
	assert.Contains(t, workflow.Jobs["test1"].Outputs, "some-b-key")
	assert.Equal(t, "${{ steps.test1_1.outputs.a_key }}", workflow.Jobs["test1"].Outputs["some_a_key"])
	assert.Equal(t, "${{ steps.test1_1.outputs.b-key }}", workflow.Jobs["test1"].Outputs["some-b-key"])
}

func TestReadWorkflow_Strategy(t *testing.T) {
	w, err := NewWorkflowPlanner("testdata/strategy/push.yml", true)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	p, err := w.PlanJob("strategy-only-max-parallel")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	assert.Equal(t, len(p.Stages), 1)         //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, len(p.Stages[0].Runs), 1) //nolint:testifylint // pre-existing issue from nektos/act

	wf := p.Stages[0].Runs[0].Workflow

	job := wf.Jobs["strategy-only-max-parallel"]
	matrixes, err := job.GetMatrixes()
	assert.NoError(t, err)                          //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, matrixes, []map[string]any{{}}) //nolint:testifylint // pre-existing issue from nektos/act
	matrix, err := job.Matrix()
	require.NoError(t, err)
	assert.Empty(t, matrix)
	assert.Equal(t, job.Strategy.MaxParallel, 2) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, job.Strategy.FailFast, true) //nolint:testifylint // pre-existing issue from nektos/act

	job = wf.Jobs["strategy-only-fail-fast"]
	matrixes, err = job.GetMatrixes()
	assert.NoError(t, err)                          //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, matrixes, []map[string]any{{}}) //nolint:testifylint // pre-existing issue from nektos/act
	matrix, err = job.Matrix()
	require.NoError(t, err)
	assert.Empty(t, matrix)
	assert.Equal(t, job.Strategy.MaxParallel, 4)  //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, job.Strategy.FailFast, false) //nolint:testifylint // pre-existing issue from nektos/act

	job = wf.Jobs["strategy-no-matrix"]
	matrixes, err = job.GetMatrixes()
	assert.NoError(t, err)                          //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, matrixes, []map[string]any{{}}) //nolint:testifylint // pre-existing issue from nektos/act
	matrix, err = job.Matrix()
	require.NoError(t, err)
	assert.Empty(t, matrix)
	assert.Equal(t, job.Strategy.MaxParallel, 2)  //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, job.Strategy.FailFast, false) //nolint:testifylint // pre-existing issue from nektos/act

	job = wf.Jobs["strategy-all"]
	matrixes, err = job.GetMatrixes()
	assert.NoError(t, err)    //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, matrixes, //nolint:testifylint // pre-existing issue from nektos/act
		[]map[string]any{
			{"datacenter": "site-c", "node-version": "14.x", "site": "staging"},
			{"datacenter": "site-c", "node-version": "16.x", "site": "staging"},
			{"datacenter": "site-d", "node-version": "16.x", "site": "staging"},
			{"php-version": 5.4},
			{"datacenter": "site-a", "node-version": "10.x", "site": "prod"},
			{"datacenter": "site-b", "node-version": "12.x", "site": "dev"},
		},
	)
	matrix, err = job.Matrix()
	require.NoError(t, err)
	assert.Equal(t, matrix, //nolint:testifylint // pre-existing issue from nektos/act
		map[string][]any{
			"datacenter": {"site-c", "site-d"},
			"exclude": {
				map[string]any{"datacenter": "site-d", "node-version": "14.x", "site": "staging"},
			},
			"include": {
				map[string]any{"php-version": 5.4},
				map[string]any{"datacenter": "site-a", "node-version": "10.x", "site": "prod"},
				map[string]any{"datacenter": "site-b", "node-version": "12.x", "site": "dev"},
			},
			"node-version": {"14.x", "16.x"},
			"site":         {"staging"},
		},
	)
	assert.Equal(t, job.Strategy.MaxParallel, 2)  //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, job.Strategy.FailFast, false) //nolint:testifylint // pre-existing issue from nektos/act
}

func TestStep_ShellCommand(t *testing.T) {
	tests := []struct {
		shell string
		want  string
	}{
		{"pwsh -v '. {0}'", "pwsh -v '. {0}'"},
		{"pwsh", "pwsh -command . '{0}'"},
		{"powershell", "powershell -command . '{0}'"},
	}
	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			got := (&Step{Shell: tt.shell}).ShellCommand()
			assert.Equal(t, got, tt.want) //nolint:testifylint // pre-existing issue from nektos/act
		})
	}
}

func TestReadWorkflow_WorkflowDispatchConfig(t *testing.T) {
	yaml := `
    name: local-action-docker-url
    `
	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch := workflow.WorkflowDispatchConfig()
	assert.Nil(t, workflowDispatch)

	yaml = `
    name: local-action-docker-url
    on: push
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.Nil(t, workflowDispatch)

	yaml = `
    name: local-action-docker-url
    on: workflow_dispatch
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.NotNil(t, workflowDispatch)
	assert.Nil(t, workflowDispatch.Inputs)

	yaml = `
    name: local-action-docker-url
    on: [push, pull_request]
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.Nil(t, workflowDispatch)

	yaml = `
    name: local-action-docker-url
    on: [push, workflow_dispatch]
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.NotNil(t, workflowDispatch)
	assert.Nil(t, workflowDispatch.Inputs)

	yaml = `
    name: local-action-docker-url
    on:
        - push
        - workflow_dispatch
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.NotNil(t, workflowDispatch)
	assert.Nil(t, workflowDispatch.Inputs)

	yaml = `
    name: local-action-docker-url
    on:
        push:
        pull_request:
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.Nil(t, workflowDispatch)

	yaml = `
    name: local-action-docker-url
    on:
        push:
        pull_request:
        workflow_dispatch:
            inputs:
                logLevel:
                    description: 'Log level'
                    required: true
                    default: 'warning'
                    type: choice
                    options:
                    - info
                    - warning
                    - debug
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.NotNil(t, workflowDispatch)
	assert.Equal(t, WorkflowDispatchInput{
		Default:     "warning",
		Description: "Log level",
		Options: []string{
			"info",
			"warning",
			"debug",
		},
		Required: true,
		Type:     "choice",
	}, workflowDispatch.Inputs["logLevel"])
}

func TestStep_UsesHash(t *testing.T) {
	type fields struct {
		Uses string
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "regular",
			fields: fields{
				Uses: "https://gitea.com/testa/testb@v3",
			},
			want: "ae437878e9f285bd7518c58664f9fabbb12d05feddd7169c01702a2a14322aa8",
		},
		{
			name: "empty",
			fields: fields{
				Uses: "",
			},
			want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Step{
				Uses: tt.fields.Uses,
			}
			assert.Equalf(t, tt.want, s.UsesHash(), "UsesHash()")
		})
	}
}

func TestNormalizeMatrixValue(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		value      any
		wantResult []any
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "array_values_pass_through",
			key:        "version",
			value:      []any{"1.0", "2.0", "3.0"},
			wantResult: []any{"1.0", "2.0", "3.0"},
			wantErr:    false,
		},
		{
			name:       "string_scalar_wrapped",
			key:        "os",
			value:      "ubuntu-latest",
			wantResult: []any{"ubuntu-latest"},
			wantErr:    false,
		},
		{
			name:       "template_expression_wrapped",
			key:        "version",
			value:      "${{ fromJson(needs.setup.outputs.versions) }}",
			wantResult: []any{"${{ fromJson(needs.setup.outputs.versions) }}"},
			wantErr:    false,
		},
		{
			name:       "integer_scalar_wrapped",
			key:        "count",
			value:      42,
			wantResult: []any{42},
			wantErr:    false,
		},
		{
			name:       "float_scalar_wrapped",
			key:        "factor",
			value:      3.14,
			wantResult: []any{3.14},
			wantErr:    false,
		},
		{
			name:       "bool_scalar_wrapped",
			key:        "enabled",
			value:      true,
			wantResult: []any{true},
			wantErr:    false,
		},
		{
			name:       "nil_scalar_wrapped",
			key:        "optional",
			value:      nil,
			wantResult: []any{nil},
			wantErr:    false,
		},
		{
			name:    "nested_map_returns_error",
			key:     "config",
			value:   map[string]any{"nested": "value"},
			wantErr: true,
			errMsg:  "has invalid nested object value",
		},
		{
			name:       "empty_array_passes_through",
			key:        "empty",
			value:      []any{},
			wantResult: []any{},
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := normalizeMatrixValue(tt.key, tt.value)

			if tt.wantErr {
				assert.Error(t, err, "should return error") //nolint:testifylint // pre-existing issue from nektos/act
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err, "should not return error") //nolint:testifylint // pre-existing issue from nektos/act
				assert.Equal(t, tt.wantResult, result, "result should match expected")
			}
		})
	}
}

func TestJobMatrix(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		wantLen int
		checkFn func(*testing.T, map[string][]any)
	}{
		{
			name: "matrix_with_arrays",
			yaml: `
name: test
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        os: [ubuntu-latest, windows-latest]
        version: [1.18, 1.19]
    steps:
      - run: echo test
`,
			wantErr: false,
			wantLen: 2,
			checkFn: func(t *testing.T, m map[string][]any) {
				assert.Equal(t, []any{"ubuntu-latest", "windows-latest"}, m["os"])
				assert.Equal(t, []any{1.18, 1.19}, m["version"])
			},
		},
		{
			name: "matrix_with_scalar_values",
			yaml: `
name: test
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        os: ubuntu-latest
        version: 1.19
    steps:
      - run: echo test
`,
			wantErr: false,
			wantLen: 2,
			checkFn: func(t *testing.T, m map[string][]any) {
				assert.Equal(t, []any{"ubuntu-latest"}, m["os"])
				assert.Equal(t, []any{1.19}, m["version"])
			},
		},
		{
			name: "matrix_with_template_expression",
			yaml: `
name: test
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        versions: ${{ fromJson(needs.setup.outputs.versions) }}
    steps:
      - run: echo test
`,
			wantErr: false,
			wantLen: 1,
			checkFn: func(t *testing.T, m map[string][]any) {
				assert.Equal(t, []any{"${{ fromJson(needs.setup.outputs.versions) }}"}, m["versions"])
			},
		},
		{
			name: "matrix_mixed_arrays_and_scalars",
			yaml: `
name: test
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        os: [ubuntu-latest, windows-latest]
        version: 1.19
        node: [14, 16]
    steps:
      - run: echo test
`,
			wantErr: false,
			wantLen: 3,
			checkFn: func(t *testing.T, m map[string][]any) {
				assert.Equal(t, []any{"ubuntu-latest", "windows-latest"}, m["os"])
				assert.Equal(t, []any{1.19}, m["version"])
				assert.Equal(t, []any{14, 16}, m["node"])
			},
		},
		{
			name: "empty_matrix",
			yaml: `
name: test
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo test
`,
			wantErr: false,
			wantLen: 0,
			checkFn: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflow, err := ReadWorkflow(strings.NewReader(tt.yaml))
			assert.NoError(t, err, "reading workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

			job := workflow.GetJob("build")
			if job == nil {
				// For empty matrix test
				if tt.wantLen == 0 {
					return
				}
				t.Fatal("job not found")
			}

			matrix, err := job.Matrix()

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, matrix, "matrix should be nil on error")
			} else {
				require.NoError(t, err)
				if tt.wantLen == 0 {
					assert.Empty(t, matrix, "no matrix for jobs without strategy")
				} else {
					assert.NotNil(t, matrix, "matrix should not be nil")
					assert.Len(t, matrix, tt.wantLen, "matrix should have expected number of keys")
					if tt.checkFn != nil {
						tt.checkFn(t, matrix)
					}
				}
			}
		})
	}
}

func TestJobMatrixValidation(t *testing.T) {
	// This test verifies that invalid nested map values are caught
	t.Run("matrix_with_nested_map_fails", func(t *testing.T) {
		// Manually construct a job with a problematic matrix containing a nested map
		job := &Job{
			Strategy: &Strategy{
				RawMatrix: yaml.Node{
					Kind: yaml.MappingNode,
					Content: []*yaml.Node{
						{Kind: yaml.ScalarNode, Tag: "!!str", Value: "config"},
						{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "nested"},
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "value"},
						}},
					},
				},
			},
		}

		matrix, err := job.Matrix()
		require.ErrorContains(t, err, `matrix key "config" has invalid nested object value`)
		assert.Nil(t, matrix)
	})
}

func readJob(t *testing.T, content string) *Job {
	t.Helper()

	var job Job
	require.NoError(t, yaml.Unmarshal([]byte(content), &job))
	return &job
}

func readStep(t *testing.T, content string) *Step {
	t.Helper()

	var step Step
	require.NoError(t, yaml.Unmarshal([]byte(content), &step))
	return &step
}
