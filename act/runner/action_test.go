// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/common/git"
	"gitea.com/gitea/runner/act/container"
	"gitea.com/gitea/runner/act/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type closerMock struct {
	mock.Mock
}

func (m *closerMock) Close() error {
	m.Called()
	return nil
}

func TestActionReader(t *testing.T) {
	yaml := strings.ReplaceAll(`
name: 'name'
runs:
	using: 'node16'
	main: 'main.js'
`, "\t", "  ")

	table := []struct {
		name        string
		step        *model.Step
		filename    string
		fileContent string
		expected    *model.Action
	}{
		{
			name:        "readActionYml",
			step:        &model.Step{},
			filename:    "action.yml",
			fileContent: yaml,
			expected: &model.Action{
				Name: "name",
				Runs: model.ActionRuns{
					Using:  "node16",
					Main:   "main.js",
					PreIf:  "always()",
					PostIf: "always()",
				},
			},
		},
		{
			name:        "readActionYaml",
			step:        &model.Step{},
			filename:    "action.yaml",
			fileContent: yaml,
			expected: &model.Action{
				Name: "name",
				Runs: model.ActionRuns{
					Using:  "node16",
					Main:   "main.js",
					PreIf:  "always()",
					PostIf: "always()",
				},
			},
		},
		{
			name:        "readDockerfile",
			step:        &model.Step{},
			filename:    "Dockerfile",
			fileContent: "FROM ubuntu:20.04",
			expected: &model.Action{
				Name: "(Synthetic)",
				Runs: model.ActionRuns{
					Using: "docker",
					Image: "Dockerfile",
				},
			},
		},
		{
			name: "readWithArgs",
			step: &model.Step{
				With: map[string]string{
					"args": "cmd",
				},
			},
			expected: &model.Action{
				Name: "(Synthetic)",
				Inputs: map[string]model.Input{
					"cwd": {
						Description: "(Actual working directory)",
						Required:    false,
						Default:     "actionDir/actionPath",
					},
					"command": {
						Description: "(Actual program)",
						Required:    false,
						Default:     "cmd",
					},
				},
				Runs: model.ActionRuns{
					Using: "node12",
					Main:  "trampoline.js",
				},
			},
		},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			closerMock := &closerMock{}

			readFile := func(filename string) (io.Reader, io.Closer, error) {
				if tt.filename != filename {
					return nil, nil, fs.ErrNotExist
				}

				return strings.NewReader(tt.fileContent), closerMock, nil
			}

			writeFile := func(filename string, data []byte, perm fs.FileMode) error {
				assert.Equal(t, "actionDir/actionPath/trampoline.js", filename)
				assert.Equal(t, fs.FileMode(0o400), perm)
				return nil
			}

			if tt.filename != "" {
				closerMock.On("Close")
			}

			action, err := readActionImpl(context.Background(), tt.step, "actionDir", "actionPath", readFile, writeFile)

			assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
			assert.Equal(t, tt.expected, action)

			closerMock.AssertExpectations(t)
		})
	}
}

// With AutoRemove the daemon reaps the container on exit, so act must not remove it afterwards.
func TestExecAsDockerAutoRemove(t *testing.T) {
	orig := ContainerNewContainer
	defer func() { ContainerNewContainer = orig }()

	for _, tc := range []struct {
		autoRemove bool
		removes    int
	}{
		{false, 2}, // stale + post-run
		{true, 1},  // post-run skipped
	} {
		cm := &containerMock{}
		ContainerNewContainer = func(*container.NewContainerInput) container.ExecutionsEnvironment { return cm }

		step := &stepActionRemote{
			Step: &model.Step{ID: "1", Uses: "org/action@v1"},
			RunContext: &RunContext{
				Config:       &Config{AutoRemove: tc.autoRemove},
				Run:          &model.Run{JobID: "1", Workflow: &model.Workflow{Jobs: map[string]*model.Job{"1": {}}}},
				JobContainer: cm,
			},
			action: &model.Action{Runs: model.ActionRuns{Using: "docker", Image: "docker://node:14"}},
		}

		removes := 0
		cm.On("Pull", false).Return(func(context.Context) error { return nil })
		cm.On("Remove").Return(func(context.Context) error { removes++; return nil })
		cm.On("Create", []string(nil), []string(nil)).Return(func(context.Context) error { return nil })
		cm.On("Start", true).Return(func(context.Context) error { return nil })
		cm.On("Close").Return(func(context.Context) error { return nil })

		require.NoError(t, execAsDocker(context.Background(), step, "action", t.TempDir(), t.TempDir(), false, stepStageMain))
		cm.AssertExpectations(t)
		assert.Equal(t, tc.removes, removes)
	}
}

func TestActionRunner(t *testing.T) {
	table := []struct {
		name        string
		step        actionStep
		expectedEnv map[string]string
	}{
		{
			name: "with-input",
			step: &stepActionRemote{
				Step: &model.Step{
					Uses: "org/repo/path@ref",
				},
				RunContext: &RunContext{
					Config: &Config{},
					Run: &model.Run{
						JobID: "job",
						Workflow: &model.Workflow{
							Jobs: map[string]*model.Job{
								"job": {
									Name: "job",
								},
							},
						},
					},
				},
				action: &model.Action{
					Inputs: map[string]model.Input{
						"key": {
							Default: "default value",
						},
					},
					Runs: model.ActionRuns{
						Using: "node16",
					},
				},
				env: map[string]string{},
			},
			expectedEnv: map[string]string{"INPUT_KEY": "default value"},
		},
		{
			name: "restore-saved-state",
			step: &stepActionRemote{
				Step: &model.Step{
					ID:   "step",
					Uses: "org/repo/path@ref",
				},
				RunContext: &RunContext{
					ActionPath: "path",
					Config:     &Config{},
					Run: &model.Run{
						JobID: "job",
						Workflow: &model.Workflow{
							Jobs: map[string]*model.Job{
								"job": {
									Name: "job",
								},
							},
						},
					},
					CurrentStep: "post-step",
					StepResults: map[string]*model.StepResult{
						"step": {},
					},
					IntraActionState: map[string]map[string]string{
						"step": {
							"name": "state value",
						},
					},
				},
				action: &model.Action{
					Runs: model.ActionRuns{
						Using: "node16",
					},
				},
				env: map[string]string{},
			},
			expectedEnv: map[string]string{"STATE_name": "state value"},
		},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			cm := &containerMock{}
			cm.On("CopyDir", "/var/run/act/actions/dir/", "dir/", false).Return(func(ctx context.Context) error { return nil })

			envMatcher := mock.MatchedBy(func(env map[string]string) bool {
				for k, v := range tt.expectedEnv {
					if env[k] != v {
						return false
					}
				}
				return true
			})

			cm.On("Exec", []string{"node", "/var/run/act/actions/dir/path"}, envMatcher, "", "").Return(func(ctx context.Context) error { return nil })

			tt.step.getRunContext().JobContainer = cm

			err := runActionImpl(tt.step, "dir", newRemoteAction("org/repo/path@ref"))(ctx)

			assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
			cm.AssertExpectations(t)
		})
	}
}

func TestNewStepContainerDoesNotUseDockerSecrets(t *testing.T) {
	cm := &containerMock{}

	var captured *container.NewContainerInput
	origContainerNewContainer := ContainerNewContainer
	ContainerNewContainer = func(input *container.NewContainerInput) container.ExecutionsEnvironment {
		captured = input
		return cm
	}
	defer func() {
		ContainerNewContainer = origContainerNewContainer
	}()

	ctx := context.Background()
	rc := &RunContext{
		Name: "job",
		Config: &Config{
			Secrets: map[string]string{
				"DOCKER_USERNAME": "docker-user",
				"DOCKER_PASSWORD": "docker-password",
			},
		},
		Run: &model.Run{
			JobID: "job",
			Workflow: &model.Workflow{
				Name: "test",
				Jobs: map[string]*model.Job{
					"job": {},
				},
			},
		},
		JobContainer: cm,
		StepResults:  map[string]*model.StepResult{},
	}
	env := map[string]string{}
	step := &stepMock{}
	step.On("getRunContext").Return(rc)
	step.On("getStepModel").Return(&model.Step{ID: "action"})
	step.On("getEnv").Return(&env)

	_ = newStepContainer(ctx, step, "registry.example.com/action:tag", nil, nil)

	// DOCKER_USERNAME/DOCKER_PASSWORD should not be injected as pull credentials for docker action containers.
	assert.Empty(t, captured.Username)
	assert.Empty(t, captured.Password)
	step.AssertExpectations(t)
}

func TestMaybeCopyToActionDirHoldsCloneLock(t *testing.T) {
	ctx := context.Background()

	actionDir := t.TempDir()

	releaseCopy := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseCopy) })
	defer release()

	copyEntered := make(chan struct{})

	cm := &containerMock{}
	cm.On("CopyDir", "/var/run/act/actions/", actionDir+"/", false).Return(func(ctx context.Context) error {
		close(copyEntered)
		<-releaseCopy
		return nil
	})

	step := &stepActionRemote{
		Step: &model.Step{Uses: "remote/action@v1"},
		RunContext: &RunContext{
			Config:       &Config{},
			JobContainer: cm,
		},
	}

	copyDone := make(chan error, 1)
	go func() {
		copyDone <- maybeCopyToActionDir(ctx, step, actionDir, "", "/var/run/act/actions/")
	}()

	select {
	case <-copyEntered:
	case err := <-copyDone:
		t.Fatalf("maybeCopyToActionDir returned before CopyDir was entered: %v", err)
	case <-time.After(time.Second):
		t.Fatal("CopyDir was not entered within 1 second")
	}

	peerAcquired := make(chan struct{})
	go func() {
		unlock := git.AcquireCloneLock(actionDir)
		close(peerAcquired)
		unlock()
	}()

	select {
	case <-peerAcquired:
		t.Fatal("peer AcquireCloneLock returned while CopyDir was running")
	case <-time.After(50 * time.Millisecond):
	}

	release()

	select {
	case err := <-copyDone:
		if err != nil {
			t.Fatalf("maybeCopyToActionDir returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("maybeCopyToActionDir did not return after CopyDir was unblocked")
	}

	select {
	case <-peerAcquired:
	case <-time.After(time.Second):
		t.Fatal("peer AcquireCloneLock did not proceed after lock released")
	}

	cm.AssertExpectations(t)
}

func TestExecAsDockerHoldsCloneLockForRemoteUncached(t *testing.T) {
	actionDir := t.TempDir()

	unlockOnce := sync.OnceFunc(git.AcquireCloneLock(actionDir))
	defer unlockOnce()

	innerEntered := make(chan struct{})
	releaseInner := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(releaseInner) })
	defer releaseOnce()

	origImageExists := ContainerImageExistsLocally
	ContainerImageExistsLocally = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}
	defer func() { ContainerImageExistsLocally = origImageExists }()

	origBuildExec := ContainerNewDockerBuildExecutor
	ContainerNewDockerBuildExecutor = func(_ container.NewDockerBuildExecutorInput) common.Executor {
		return func(_ context.Context) error {
			close(innerEntered)
			<-releaseInner
			return nil
		}
	}
	defer func() { ContainerNewDockerBuildExecutor = origBuildExec }()

	step := &stepActionRemote{
		Step: &model.Step{ID: "1", Uses: "remote/action@v1", With: map[string]string{}},
		RunContext: &RunContext{
			Config: &Config{},
			Run: &model.Run{
				JobID: "1",
				Workflow: &model.Workflow{
					Name: "wf",
					Jobs: map[string]*model.Job{"1": {}},
				},
			},
			JobContainer: &containerMock{},
		},
		action: &model.Action{Runs: model.ActionRuns{Using: "docker", Image: "Dockerfile"}},
		env:    map[string]string{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- execAsDocker(ctx, step, "test-action", actionDir, actionDir, false, stepStageMain) }()

	select {
	case <-innerEntered:
		t.Fatal("inner build executor ran before clone lock was released")
	case err := <-done:
		t.Fatalf("execAsDocker returned before inner was entered: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	unlockOnce()

	select {
	case <-innerEntered:
	case err := <-done:
		t.Fatalf("execAsDocker returned without entering inner: %v", err)
	case <-time.After(time.Second):
		t.Fatal("inner build executor not entered after lock released")
	}

	cancel()
	releaseOnce()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("execAsDocker did not return after inner was released and ctx was canceled")
	}
}

func TestDockerActionImageTag(t *testing.T) {
	// Remote actions already carry a unique, ref-scoped actionName (the uses
	// hash), so the tag must be left untouched for backwards compatibility.
	assert.Equal(t,
		"act-abc123-dockeraction:latest",
		dockerActionImageTag("owner/repo", "abc123", false),
	)

	// Local actions keep a human-readable, repository-namespaced prefix and gain a short hash suffix that makes the tag unique per (repository, actionName).
	// See https://gitea.com/gitea/runner/issues/1039.
	assert.Equal(t,
		"act-owner-repo-baca2daaa2fe-dockeraction:latest",
		dockerActionImageTag("owner/repo", "./", true),
	)
	assert.Equal(t,
		"act-owner-repo-sub-e847b61255a8-dockeraction:latest",
		dockerActionImageTag("owner/repo", "./sub", true),
	)

	// Sanitizing every non-alphanumeric character to "-" is lossy, so distinct inputs can collapse to the same readable prefix.
	// The hash suffix must keep such cases apart, otherwise an image built for one repository is reused for another.
	collisions := [][2]struct {
		repoName   string
		actionName string
	}{
		// Two different repositories, both `uses: ./`: "a/b-c" and "a-b/c" both sanitize to "a-b-c".
		{{"a/b-c", "./"}, {"a-b/c", "./"}},
		// A repository's root action vs another repository's sub-path action:
		// "owner/repo-a" + "./" and "owner/repo" + "./a" both sanitize to "owner-repo-a".
		{{"owner/repo-a", "./"}, {"owner/repo", "./a"}},
	}
	for _, c := range collisions {
		assert.NotEqual(t,
			dockerActionImageTag(c[0].repoName, c[0].actionName, true),
			dockerActionImageTag(c[1].repoName, c[1].actionName, true),
			"local docker action tags must differ for %q/%q vs %q/%q",
			c[0].repoName, c[0].actionName, c[1].repoName, c[1].actionName,
		)
	}

	// Distinct local actions within the same repository keep distinct tags.
	assert.NotEqual(t,
		dockerActionImageTag("owner/repo", "./", true),
		dockerActionImageTag("owner/repo", "./sub", true),
	)
}

// Only the entrypoint is stage specific: every stage of a docker action receives runs.args
// and runs.env, and the `entrypoint` input applies to the main stage alone.
func TestExecAsDockerStageEntrypoint(t *testing.T) {
	orig := ContainerNewContainer
	defer func() { ContainerNewContainer = orig }()

	for _, tc := range []struct {
		name           string
		stage          stepStage
		wantEntrypoint []string
	}{
		{
			name:           "main stage prefers the entrypoint input",
			stage:          stepStageMain,
			wantEntrypoint: []string{"input.sh"},
		},
		{
			name:           "pre stage uses runs.pre-entrypoint",
			stage:          stepStagePre,
			wantEntrypoint: []string{"pre.sh", "--verbose"},
		},
		{
			name:           "post stage uses runs.post-entrypoint",
			stage:          stepStagePost,
			wantEntrypoint: []string{"post.sh"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm := &containerMock{}
			var input *container.NewContainerInput
			ContainerNewContainer = func(in *container.NewContainerInput) container.ExecutionsEnvironment {
				input = in
				return cm
			}

			step := &stepActionRemote{
				Step: &model.Step{ID: "1", Uses: "org/action@v1", With: map[string]string{"entrypoint": "input.sh"}},
				RunContext: &RunContext{
					Config:       &Config{},
					Run:          &model.Run{JobID: "1", Workflow: &model.Workflow{Jobs: map[string]*model.Job{"1": {}}}},
					JobContainer: cm,
				},
				action: &model.Action{Runs: model.ActionRuns{
					Using:          "docker",
					Image:          "docker://node:14",
					PreEntrypoint:  "pre.sh --verbose",
					Entrypoint:     "main.sh",
					PostEntrypoint: "post.sh",
					Args:           []string{"hello"},
					Env:            map[string]string{"MY_VAR": "world"},
				}},
				env: map[string]string{},
			}

			cm.On("Pull", false).Return(func(context.Context) error { return nil })
			cm.On("Remove").Return(func(context.Context) error { return nil })
			cm.On("Create", []string(nil), []string(nil)).Return(func(context.Context) error { return nil })
			cm.On("Start", true).Return(func(context.Context) error { return nil })
			cm.On("Close").Return(func(context.Context) error { return nil })

			require.NoError(t, execAsDocker(context.Background(), step, "action", t.TempDir(), t.TempDir(), false, tc.stage))
			require.NotNil(t, input)
			assert.Equal(t, tc.wantEntrypoint, input.Entrypoint)
			assert.Equal(t, []string{"hello"}, input.Cmd)
			assert.Contains(t, input.Env, "MY_VAR=world")
		})
	}
}

func TestDockerActionHasPreAndPostStep(t *testing.T) {
	newStep := func(runs model.ActionRuns) actionStep {
		return &stepActionRemote{action: &model.Action{Runs: runs}}
	}
	ctx := context.Background()

	assert.False(t, hasPreStep(newStep(model.ActionRuns{Using: "docker", Image: "Dockerfile"}))(ctx))
	assert.False(t, hasPostStep(newStep(model.ActionRuns{Using: "docker", Image: "Dockerfile"}))(ctx))

	withStages := model.ActionRuns{Using: "docker", Image: "Dockerfile", PreEntrypoint: "pre.sh", PostEntrypoint: "post.sh"}
	assert.True(t, hasPreStep(newStep(withStages))(ctx))
	assert.True(t, hasPostStep(newStep(withStages))(ctx))
}
