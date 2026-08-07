// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitea.com/gitea/runner/act/model"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
)

func TestExecuteArgsResolve(t *testing.T) {
	workdir := t.TempDir()
	args := &executeArgs{workdir: workdir}

	require.Empty(t, args.resolve(""))
	require.Equal(t, filepath.Join(workdir, "sub", "file"), args.resolve("sub/file"))

	abs := filepath.Join(workdir, "abs")
	require.Equal(t, abs, args.resolve(abs))
}

func TestExecuteArgsPaths(t *testing.T) {
	workdir := t.TempDir()
	args := &executeArgs{
		workdir:       workdir,
		workflowsPath: ".gitea/workflows",
		envfile:       ".env",
	}

	require.Equal(t, filepath.Join(workdir, ".gitea/workflows"), args.WorkflowsPath())
	require.Equal(t, filepath.Join(workdir, ".env"), args.Envfile())
	require.Equal(t, workdir, args.Workdir())
}

func TestExecuteArgsLoadVars(t *testing.T) {
	require.Empty(t, (&executeArgs{}).LoadVars())

	args := &executeArgs{vars: []string{"FOO=bar", "EMPTY", "WITH=eq=sign"}}
	require.Equal(t, map[string]string{
		"FOO":   "bar",
		"EMPTY": "",
		"WITH":  "eq=sign",
	}, args.LoadVars())
}

func TestExecuteArgsLoadSecrets(t *testing.T) {
	t.Setenv("FROMENV", "from-env-value")

	args := &executeArgs{secrets: []string{"token=abc", "fromenv"}}
	require.Equal(t, map[string]string{
		"TOKEN":   "abc",
		"FROMENV": "from-env-value",
	}, args.LoadSecrets())
}

func TestReadEnvs(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("FOO=bar\nBAZ=qux\n"), 0o600))

	envs := map[string]string{"EXISTING": "keep"}
	require.True(t, readEnvs(envFile, envs))
	require.Equal(t, map[string]string{
		"EXISTING": "keep",
		"FOO":      "bar",
		"BAZ":      "qux",
	}, envs)

	missing := map[string]string{}
	require.False(t, readEnvs(filepath.Join(dir, "does-not-exist"), missing))
	require.Empty(t, missing)
}

func TestRunExecListUsesJobEventAndAllPlans(t *testing.T) {
	planner := &fakeWorkflowPlanner{
		events: []string{"push", "pull_request"},
		plans: map[string]*model.Plan{
			"job:build":  listPlan("build", "Build", "push"),
			"event:push": listPlan("test", "Test", "push"),
			"all":        listPlan("lint", "Lint", "push"),
		},
	}

	out := captureStdout(t, func() {
		require.NoError(t, runExecList(planner, &executeArgs{job: "build"}))
		require.NoError(t, runExecList(planner, &executeArgs{event: "push"}))
		require.NoError(t, runExecList(planner, &executeArgs{autodetectEvent: true}))
		require.NoError(t, runExecList(planner, &executeArgs{}))
	})

	require.Contains(t, out, "Build")
	require.Contains(t, out, "Test")
	require.Contains(t, out, "Lint")
	require.Equal(t, []string{"job:build", "event:push", "event:push", "all"}, planner.calls)
}

func TestPrintListReportsDuplicateJobIDs(t *testing.T) {
	workflowA := workflowForList("A", "a.yml", "push", "build", "Build A")
	workflowB := workflowForList("B", "b.yml", "pull_request", "build", "Build B")
	plan := &model.Plan{Stages: []*model.Stage{{
		Runs: []*model.Run{
			{Workflow: workflowA, JobID: "build"},
			{Workflow: workflowB, JobID: "build"},
		},
	}}}

	out := captureStdout(t, func() {
		printList(plan)
	})

	require.Contains(t, out, "Workflow file")
	require.Contains(t, out, "Build A")
	require.Contains(t, out, "Build B")
	require.Contains(t, out, "Detected multiple jobs with the same job name")
}

func TestLoadExecCmdDefinesExpectedFlags(t *testing.T) {
	cmd := loadExecCmd(context.Background())

	for _, name := range []string{
		"list",
		"job",
		"event",
		"workflows",
		"directory",
		"env",
		"secret",
		"var",
		"dryrun",
		"image",
		"gitea-instance",
	} {
		if cmd.Flags().Lookup(name) == nil && cmd.PersistentFlags().Lookup(name) == nil {
			t.Fatalf("expected flag %q to be registered", name)
		}
	}

	require.Equal(t, "exec", cmd.Use)
	require.NoError(t, cmd.Args(cmd, strings.Split("a b c", " ")))
	require.Error(t, cmd.Args(cmd, strings.Fields(strings.Repeat("arg ", 21))))
}

type fakeWorkflowPlanner struct {
	events []string
	plans  map[string]*model.Plan
	calls  []string
}

func (p *fakeWorkflowPlanner) PlanEvent(eventName string) (*model.Plan, error) {
	p.calls = append(p.calls, "event:"+eventName)
	return p.plans["event:"+eventName], nil
}

func (p *fakeWorkflowPlanner) PlanJob(jobName string) (*model.Plan, error) {
	p.calls = append(p.calls, "job:"+jobName)
	return p.plans["job:"+jobName], nil
}

func (p *fakeWorkflowPlanner) PlanAll() (*model.Plan, error) {
	p.calls = append(p.calls, "all")
	return p.plans["all"], nil
}

func (p *fakeWorkflowPlanner) GetEvents() []string {
	return p.events
}

func listPlan(jobID, jobName, event string) *model.Plan {
	workflow := workflowForList("Workflow "+jobID, jobID+".yml", event, jobID, jobName)
	return &model.Plan{Stages: []*model.Stage{{Runs: []*model.Run{{Workflow: workflow, JobID: jobID}}}}}
}

func workflowForList(name, file, event, jobID, jobName string) *model.Workflow {
	return &model.Workflow{
		Name:  name,
		File:  file,
		RawOn: rawOnNode(event),
		Jobs: map[string]*model.Job{
			jobID: {Name: jobName},
		},
	}
}

func rawOnNode(event string) yaml.Node {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(event), &node); err != nil {
		panic(err)
	}
	return *node.Content[0]
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	fn()

	require.NoError(t, w.Close())
	os.Stdout = old

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	return buf.String()
}
