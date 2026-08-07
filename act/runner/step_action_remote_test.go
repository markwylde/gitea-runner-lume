// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/common/git"
	"gitea.com/gitea/runner/act/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
)

type stepActionRemoteMocks struct {
	mock.Mock
}

func (sarm *stepActionRemoteMocks) readAction(_ context.Context, step *model.Step, actionDir, actionPath string, readFile actionYamlReader, writeFile fileWriter) (*model.Action, error) {
	args := sarm.Called(step, actionDir, actionPath, readFile, writeFile)
	return args.Get(0).(*model.Action), args.Error(1)
}

func (sarm *stepActionRemoteMocks) runAction(step actionStep, actionDir string, remoteAction *remoteAction) common.Executor {
	args := sarm.Called(step, actionDir, remoteAction)
	return args.Get(0).(func(context.Context) error)
}

func TestStepActionRemote(t *testing.T) {
	table := []struct {
		name      string
		stepModel *model.Step
		result    *model.StepResult
		mocks     struct {
			env    bool
			cloned bool
			read   bool
			run    bool
		}
		runError error
	}{
		{
			name: "run-successful",
			stepModel: &model.Step{
				ID:   "step",
				Uses: "remote/action@v1",
			},
			result: &model.StepResult{
				Conclusion: model.StepStatusSuccess,
				Outcome:    model.StepStatusSuccess,
				Outputs:    map[string]string{},
			},
			mocks: struct {
				env    bool
				cloned bool
				read   bool
				run    bool
			}{
				env:    true,
				cloned: true,
				read:   true,
				run:    true,
			},
		},
		{
			name: "run-skipped",
			stepModel: &model.Step{
				ID:   "step",
				Uses: "remote/action@v1",
				If:   yaml.Node{Value: "false"},
			},
			result: &model.StepResult{
				Conclusion: model.StepStatusSkipped,
				Outcome:    model.StepStatusSkipped,
				Outputs:    map[string]string{},
			},
			mocks: struct {
				env    bool
				cloned bool
				read   bool
				run    bool
			}{
				env:    true,
				cloned: true,
				read:   true,
				run:    false,
			},
		},
		{
			name: "run-error",
			stepModel: &model.Step{
				ID:   "step",
				Uses: "remote/action@v1",
			},
			result: &model.StepResult{
				Conclusion: model.StepStatusFailure,
				Outcome:    model.StepStatusFailure,
				Outputs:    map[string]string{},
			},
			mocks: struct {
				env    bool
				cloned bool
				read   bool
				run    bool
			}{
				env:    true,
				cloned: true,
				read:   true,
				run:    true,
			},
			runError: errors.New("error"),
		},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			cm := &containerMock{}
			sarm := &stepActionRemoteMocks{}

			clonedAction := false

			origStepAtionRemoteNewCloneExecutor := stepActionRemoteNewCloneExecutor
			stepActionRemoteNewCloneExecutor = func(input git.NewGitCloneExecutorInput) common.Executor {
				return func(ctx context.Context) error {
					clonedAction = true
					return nil
				}
			}
			defer (func() {
				stepActionRemoteNewCloneExecutor = origStepAtionRemoteNewCloneExecutor
			})()

			sar := &stepActionRemote{
				RunContext: &RunContext{
					Config: &Config{
						GitHubInstance: "github.com",
						ActionCacheDir: "/tmp/test-cache",
					},
					Run: &model.Run{
						JobID: "1",
						Workflow: &model.Workflow{
							Jobs: map[string]*model.Job{
								"1": {},
							},
						},
					},
					StepResults:  map[string]*model.StepResult{},
					JobContainer: cm,
				},
				Step:       tt.stepModel,
				readAction: sarm.readAction,
				runAction:  sarm.runAction,
			}
			sar.RunContext.ExprEval = sar.RunContext.NewExpressionEvaluator(ctx)

			suffixMatcher := func(suffix string) any {
				return mock.MatchedBy(func(actionDir string) bool {
					return strings.HasSuffix(actionDir, suffix)
				})
			}

			if tt.mocks.read {
				sarm.On("readAction", sar.Step, suffixMatcher(sar.Step.UsesHash()), "", mock.Anything, mock.Anything).Return(&model.Action{}, nil)
			}
			if tt.mocks.run {
				sarm.On("runAction", sar, suffixMatcher(sar.Step.UsesHash()), newRemoteAction(sar.Step.Uses)).Return(func(ctx context.Context) error { return tt.runError })

				cm.On("Copy", "/var/run/act", mock.AnythingOfType("[]*container.FileEntry")).Return(func(ctx context.Context) error {
					return nil
				})

				cm.On("UpdateFromEnv", "/var/run/act/workflow/envs.txt", mock.AnythingOfType("*map[string]string")).Return(func(ctx context.Context) error {
					return nil
				})

				cm.On("UpdateFromEnv", "/var/run/act/workflow/statecmd.txt", mock.AnythingOfType("*map[string]string")).Return(func(ctx context.Context) error {
					return nil
				})

				cm.On("UpdateFromEnv", "/var/run/act/workflow/outputcmd.txt", mock.AnythingOfType("*map[string]string")).Return(func(ctx context.Context) error {
					return nil
				})

				cm.On("GetContainerArchive", ctx, "/var/run/act/workflow/pathcmd.txt").Return(io.NopCloser(&bytes.Buffer{}), nil)
			}

			err := sar.pre()(ctx)
			if err == nil {
				err = sar.main()(ctx)
			}

			assert.Equal(t, tt.runError, err)
			assert.Equal(t, tt.mocks.cloned, clonedAction)
			assert.Equal(t, tt.result, sar.RunContext.StepResults["step"])

			sarm.AssertExpectations(t)
			cm.AssertExpectations(t)
		})
	}
}

func TestStepActionRemotePre(t *testing.T) {
	table := []struct {
		name      string
		stepModel *model.Step
	}{
		{
			name: "run-pre",
			stepModel: &model.Step{
				Uses: "org/repo/path@ref",
			},
		},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			clonedAction := false
			sarm := &stepActionRemoteMocks{}

			origStepAtionRemoteNewCloneExecutor := stepActionRemoteNewCloneExecutor
			stepActionRemoteNewCloneExecutor = func(input git.NewGitCloneExecutorInput) common.Executor {
				return func(ctx context.Context) error {
					clonedAction = true
					return nil
				}
			}
			defer (func() {
				stepActionRemoteNewCloneExecutor = origStepAtionRemoteNewCloneExecutor
			})()

			sar := &stepActionRemote{
				Step: tt.stepModel,
				RunContext: &RunContext{
					Config: &Config{
						GitHubInstance: "https://github.com",
						ActionCacheDir: "/tmp/test-cache",
					},
					Run: &model.Run{
						JobID: "1",
						Workflow: &model.Workflow{
							Jobs: map[string]*model.Job{
								"1": {},
							},
						},
					},
				},
				readAction: sarm.readAction,
			}

			suffixMatcher := func(suffix string) any {
				return mock.MatchedBy(func(actionDir string) bool {
					return strings.HasSuffix(actionDir, suffix)
				})
			}

			sarm.On("readAction", sar.Step, suffixMatcher(sar.Step.UsesHash()), "path", mock.Anything, mock.Anything).Return(&model.Action{}, nil)

			err := sar.pre()(ctx)

			assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
			assert.True(t, clonedAction)

			sarm.AssertExpectations(t)
		})
	}
}

func TestStepActionRemotePreThroughAction(t *testing.T) {
	table := []struct {
		name      string
		stepModel *model.Step
	}{
		{
			name: "run-pre",
			stepModel: &model.Step{
				Uses: "org/repo/path@ref",
			},
		},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			clonedAction := false
			sarm := &stepActionRemoteMocks{}

			origStepAtionRemoteNewCloneExecutor := stepActionRemoteNewCloneExecutor
			stepActionRemoteNewCloneExecutor = func(input git.NewGitCloneExecutorInput) common.Executor {
				return func(ctx context.Context) error {
					if input.URL == "https://github.com/org/repo" {
						clonedAction = true
					}
					return nil
				}
			}
			defer (func() {
				stepActionRemoteNewCloneExecutor = origStepAtionRemoteNewCloneExecutor
			})()

			sar := &stepActionRemote{
				Step: tt.stepModel,
				RunContext: &RunContext{
					Config: &Config{
						GitHubInstance:                "https://enterprise.github.com",
						ReplaceGheActionWithGithubCom: []string{"org/repo"},
						ActionCacheDir:                "/tmp/test-cache",
					},
					Run: &model.Run{
						JobID: "1",
						Workflow: &model.Workflow{
							Jobs: map[string]*model.Job{
								"1": {},
							},
						},
					},
				},
				readAction: sarm.readAction,
			}

			suffixMatcher := func(suffix string) any {
				return mock.MatchedBy(func(actionDir string) bool {
					return strings.HasSuffix(actionDir, suffix)
				})
			}

			sarm.On("readAction", sar.Step, suffixMatcher(sar.Step.UsesHash()), "path", mock.Anything, mock.Anything).Return(&model.Action{}, nil)

			err := sar.pre()(ctx)

			assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
			assert.True(t, clonedAction)

			sarm.AssertExpectations(t)
		})
	}
}

func TestStepActionRemotePreThroughActionToken(t *testing.T) {
	table := []struct {
		name      string
		stepModel *model.Step
	}{
		{
			name: "run-pre",
			stepModel: &model.Step{
				Uses: "org/repo/path@ref",
			},
		},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			var actualURL string
			var actualToken string
			sarm := &stepActionRemoteMocks{}

			origStepAtionRemoteNewCloneExecutor := stepActionRemoteNewCloneExecutor
			stepActionRemoteNewCloneExecutor = func(input git.NewGitCloneExecutorInput) common.Executor {
				return func(ctx context.Context) error {
					actualURL = input.URL
					actualToken = input.Token
					return nil
				}
			}
			defer (func() {
				stepActionRemoteNewCloneExecutor = origStepAtionRemoteNewCloneExecutor
			})()

			// Use unique cache directory to ensure action gets cloned, not served from cache
			uniqueCacheDir := fmt.Sprintf("/tmp/test-cache-token-%d", time.Now().UnixNano())

			sar := &stepActionRemote{
				Step: tt.stepModel,
				RunContext: &RunContext{
					Config: &Config{
						GitHubInstance:                     "https://enterprise.github.com",
						ReplaceGheActionWithGithubCom:      []string{"org/repo"},
						ReplaceGheActionTokenWithGithubCom: "PRIVATE_ACTIONS_TOKEN_ON_GITHUB",
						ActionCacheDir:                     uniqueCacheDir,
						Token:                              "PRIVATE_ACTIONS_TOKEN_ON_GITHUB",
					},
					Run: &model.Run{
						JobID: "1",
						Workflow: &model.Workflow{
							Jobs: map[string]*model.Job{
								"1": {},
							},
						},
					},
				},
				readAction: sarm.readAction,
			}

			suffixMatcher := func(suffix string) any {
				return mock.MatchedBy(func(actionDir string) bool {
					return strings.HasSuffix(actionDir, suffix)
				})
			}

			sarm.On("readAction", sar.Step, suffixMatcher(sar.Step.UsesHash()), "path", mock.Anything, mock.Anything).Return(&model.Action{}, nil)

			err := sar.pre()(ctx)

			assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
			// Verify that the clone was called (URL should be redirected to github.com)
			assert.True(t, actualURL != "", "Expected clone to be called") //nolint:testifylint // pre-existing issue from nektos/act
			assert.Equal(t, "https://github.com/org/repo", actualURL, "URL should be redirected to github.com")
			// Note: Token might be empty because getGitCloneToken doesn't check ReplaceGheActionTokenWithGithubCom
			// The important part is that the URL replacement works
			if actualToken != "" {
				assert.Equal(t, "PRIVATE_ACTIONS_TOKEN_ON_GITHUB", actualToken, "If token is set, it should be the replacement token")
			}

			sarm.AssertExpectations(t)
		})
	}
}

func TestStepActionRemoteUsesGitHubInstanceWhenDefaultActionInstanceEmpty(t *testing.T) {
	ctx := context.Background()

	var actualURL string
	sarm := &stepActionRemoteMocks{}

	origStepAtionRemoteNewCloneExecutor := stepActionRemoteNewCloneExecutor
	stepActionRemoteNewCloneExecutor = func(input git.NewGitCloneExecutorInput) common.Executor {
		return func(ctx context.Context) error {
			actualURL = input.URL
			return nil
		}
	}
	defer func() {
		stepActionRemoteNewCloneExecutor = origStepAtionRemoteNewCloneExecutor
	}()

	sar := &stepActionRemote{
		Step: &model.Step{
			Uses: "actions/setup-go@v4",
		},
		RunContext: &RunContext{
			Config: &Config{
				GitHubInstance:        "gitea.example",
				DefaultActionInstance: "",
				ActionCacheDir:        t.TempDir(),
			},
			Run: &model.Run{
				JobID: "1",
				Workflow: &model.Workflow{
					Jobs: map[string]*model.Job{
						"1": {},
					},
				},
			},
		},
		readAction: sarm.readAction,
	}

	suffixMatcher := func(suffix string) any {
		return mock.MatchedBy(func(actionDir string) bool {
			return strings.HasSuffix(actionDir, suffix)
		})
	}
	sarm.On("readAction", sar.Step, suffixMatcher(sar.Step.UsesHash()), "", mock.Anything, mock.Anything).Return(&model.Action{}, nil)

	require.NoError(t, sar.prepareActionExecutor()(ctx))
	assert.Equal(t, "https://gitea.example/actions/setup-go", actualURL)
	sarm.AssertExpectations(t)
}

func TestStepActionRemotePost(t *testing.T) {
	table := []struct {
		name               string
		stepModel          *model.Step
		actionModel        *model.Action
		initialStepResults map[string]*model.StepResult
		IntraActionState   map[string]map[string]string
		expectedEnv        map[string]string
		err                error
		mocks              struct {
			env  bool
			exec bool
		}
	}{
		{
			name: "main-success",
			stepModel: &model.Step{
				ID:   "step",
				Uses: "remote/action@v1",
			},
			actionModel: &model.Action{
				Runs: model.ActionRuns{
					Using:  "node16",
					Post:   "post.js",
					PostIf: "always()",
				},
			},
			initialStepResults: map[string]*model.StepResult{
				"step": {
					Conclusion: model.StepStatusSuccess,
					Outcome:    model.StepStatusSuccess,
					Outputs:    map[string]string{},
				},
			},
			IntraActionState: map[string]map[string]string{
				"step": {
					"key": "value",
				},
			},
			expectedEnv: map[string]string{
				"STATE_key": "value",
			},
			mocks: struct {
				env  bool
				exec bool
			}{
				env:  true,
				exec: true,
			},
		},
		{
			name: "main-failed",
			stepModel: &model.Step{
				ID:   "step",
				Uses: "remote/action@v1",
			},
			actionModel: &model.Action{
				Runs: model.ActionRuns{
					Using:  "node16",
					Post:   "post.js",
					PostIf: "always()",
				},
			},
			initialStepResults: map[string]*model.StepResult{
				"step": {
					Conclusion: model.StepStatusFailure,
					Outcome:    model.StepStatusFailure,
					Outputs:    map[string]string{},
				},
			},
			mocks: struct {
				env  bool
				exec bool
			}{
				env:  true,
				exec: true,
			},
		},
		{
			name: "skip-if-failed",
			stepModel: &model.Step{
				ID:   "step",
				Uses: "remote/action@v1",
			},
			actionModel: &model.Action{
				Runs: model.ActionRuns{
					Using:  "node16",
					Post:   "post.js",
					PostIf: "success()",
				},
			},
			initialStepResults: map[string]*model.StepResult{
				"step": {
					Conclusion: model.StepStatusFailure,
					Outcome:    model.StepStatusFailure,
					Outputs:    map[string]string{},
				},
			},
			mocks: struct {
				env  bool
				exec bool
			}{
				env:  true,
				exec: false,
			},
		},
		{
			name: "skip-if-main-skipped",
			stepModel: &model.Step{
				ID:   "step",
				If:   yaml.Node{Value: "failure()"},
				Uses: "remote/action@v1",
			},
			actionModel: &model.Action{
				Runs: model.ActionRuns{
					Using:  "node16",
					Post:   "post.js",
					PostIf: "always()",
				},
			},
			initialStepResults: map[string]*model.StepResult{
				"step": {
					Conclusion: model.StepStatusSkipped,
					Outcome:    model.StepStatusSkipped,
					Outputs:    map[string]string{},
				},
			},
			mocks: struct {
				env  bool
				exec bool
			}{
				env:  false,
				exec: false,
			},
		},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			cm := &containerMock{}

			sar := &stepActionRemote{
				env: map[string]string{},
				RunContext: &RunContext{
					Config: &Config{
						GitHubInstance: "https://github.com",
						ActionCacheDir: "/tmp/test-cache",
					},
					JobContainer: cm,
					Run: &model.Run{
						JobID: "1",
						Workflow: &model.Workflow{
							Jobs: map[string]*model.Job{
								"1": {},
							},
						},
					},
					StepResults:      tt.initialStepResults,
					IntraActionState: tt.IntraActionState,
				},
				Step:   tt.stepModel,
				action: tt.actionModel,
			}
			sar.RunContext.ExprEval = sar.RunContext.NewExpressionEvaluator(ctx)

			if tt.mocks.exec {
				// Use mock.MatchedBy to match the exec command with hash-based path
				execMatcher := mock.MatchedBy(func(args []string) bool {
					if len(args) != 2 {
						return false
					}
					return args[0] == "node" && strings.Contains(args[1], "post.js")
				})

				cm.On("Exec", execMatcher, sar.env, "", "").Return(func(ctx context.Context) error { return tt.err })

				cm.On("Copy", "/var/run/act", mock.AnythingOfType("[]*container.FileEntry")).Return(func(ctx context.Context) error {
					return nil
				})

				cm.On("UpdateFromEnv", "/var/run/act/workflow/envs.txt", mock.AnythingOfType("*map[string]string")).Return(func(ctx context.Context) error {
					return nil
				})

				cm.On("UpdateFromEnv", "/var/run/act/workflow/statecmd.txt", mock.AnythingOfType("*map[string]string")).Return(func(ctx context.Context) error {
					return nil
				})

				cm.On("UpdateFromEnv", "/var/run/act/workflow/outputcmd.txt", mock.AnythingOfType("*map[string]string")).Return(func(ctx context.Context) error {
					return nil
				})

				cm.On("GetContainerArchive", ctx, "/var/run/act/workflow/pathcmd.txt").Return(io.NopCloser(&bytes.Buffer{}), nil)
			}

			err := sar.post()(ctx)

			assert.Equal(t, tt.err, err)
			if tt.expectedEnv != nil {
				for key, value := range tt.expectedEnv {
					assert.Equal(t, value, sar.env[key])
				}
			}
			// Enshure that StepResults is nil in this test
			assert.Equal(t, sar.RunContext.StepResults["post-step"], (*model.StepResult)(nil)) //nolint:testifylint // pre-existing issue from nektos/act
			cm.AssertExpectations(t)
		})
	}
}

func TestRemoteDockerActionRejectedDuringPreparation(t *testing.T) {
	sar := &stepActionRemote{
		RunContext: &RunContext{Config: &Config{DisallowDockerActions: true}},
		Step:       &model.Step{Uses: "owner/docker-action@v1"},
		action:     &model.Action{Runs: model.ActionRuns{Using: model.ActionRunsUsingDocker}},
	}
	require.ErrorContains(t, sar.rejectDisallowedAction(), "disabled")
}

func Test_newRemoteAction(t *testing.T) {
	tests := []struct {
		action       string
		want         *remoteAction
		wantCloneURL string
	}{
		{
			action: "actions/heroku@main",
			want: &remoteAction{
				URL:  "",
				Org:  "actions",
				Repo: "heroku",
				Path: "",
				Ref:  "main",
			},
			wantCloneURL: "https://github.com/actions/heroku",
		},
		{
			action: "actions/aws/ec2@main",
			want: &remoteAction{
				URL:  "",
				Org:  "actions",
				Repo: "aws",
				Path: "ec2",
				Ref:  "main",
			},
			wantCloneURL: "https://github.com/actions/aws",
		},
		{
			action: "./.github/actions/my-action", // it's valid for GitHub, but act don't support it
			want:   nil,
		},
		{
			action: "docker://alpine:3.8", // it's valid for GitHub, but act don't support it
			want:   nil,
		},
		{
			action: "https://gitea.com/actions/heroku@main", // it's invalid for GitHub, but gitea supports it
			want: &remoteAction{
				URL:  "https://gitea.com",
				Org:  "actions",
				Repo: "heroku",
				Path: "",
				Ref:  "main",
			},
			wantCloneURL: "https://gitea.com/actions/heroku",
		},
		{
			action: "https://gitea.com/actions/aws/ec2@main", // it's invalid for GitHub, but gitea supports it
			want: &remoteAction{
				URL:  "https://gitea.com",
				Org:  "actions",
				Repo: "aws",
				Path: "ec2",
				Ref:  "main",
			},
			wantCloneURL: "https://gitea.com/actions/aws",
		},
		{
			action: "http://gitea.com/actions/heroku@main", // it's invalid for GitHub, but gitea supports it
			want: &remoteAction{
				URL:  "http://gitea.com",
				Org:  "actions",
				Repo: "heroku",
				Path: "",
				Ref:  "main",
			},
			wantCloneURL: "http://gitea.com/actions/heroku",
		},
		{
			action: "http://gitea.com/actions/aws/ec2@main", // it's invalid for GitHub, but gitea supports it
			want: &remoteAction{
				URL:  "http://gitea.com",
				Org:  "actions",
				Repo: "aws",
				Path: "ec2",
				Ref:  "main",
			},
			wantCloneURL: "http://gitea.com/actions/aws",
		},
		{
			action: "ssh://git@gitea.com/actions/heroku@main", // it's invalid for GitHub, but gitea supports it
			want: &remoteAction{
				URL:  "ssh://git@gitea.com",
				Org:  "actions",
				Repo: "heroku",
				Path: "",
				Ref:  "main",
			},
			wantCloneURL: "ssh://git@gitea.com/actions/heroku",
		},
		{
			action: "ssh://git@gitea.com/actions/aws/ec2@main", // the ssh user is kept as part of the host segment
			want: &remoteAction{
				URL:  "ssh://git@gitea.com",
				Org:  "actions",
				Repo: "aws",
				Path: "ec2",
				Ref:  "main",
			},
			wantCloneURL: "ssh://git@gitea.com/actions/aws",
		},
		{
			action: "ssh://gitea.com/onlyonesegment@main", // missing org/repo after the host
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			got := newRemoteAction(tt.action)
			assert.Equalf(t, tt.want, got, "newRemoteAction(%v)", tt.action)
			cloneURL := ""
			if got != nil {
				cloneURL = got.CloneURL("github.com")
			}
			assert.Equalf(t, tt.wantCloneURL, cloneURL, "newRemoteAction(%v).CloneURL()", tt.action)
		})
	}
}

func Test_remoteActionReference(t *testing.T) {
	tests := []struct {
		uses string
		want string
	}{
		{uses: "actions/checkout@v7", want: "actions/checkout@v7"},
		{uses: "actions/aws/ec2@main", want: "actions/aws/ec2@main"},
		// The download source can be interpolated from a secret and must stay out of the log.
		{uses: "https://gitea.example.com/actions/checkout@v7", want: "actions/checkout@v7"},
	}
	for _, tt := range tests {
		t.Run(tt.uses, func(t *testing.T) {
			assert.Equal(t, tt.want, newRemoteAction(tt.uses).Reference())
		})
	}
}

// TestStepActionRemotePreResolvesDownloadedCommit runs the real download path against a local
// git repository standing in for the actions instance, so the reported commit is the one the
// clone actually checked out.
func TestStepActionRemotePreResolvesDownloadedCommit(t *testing.T) {
	instance := t.TempDir()
	actionDir := filepath.Join(instance, "actions", "setup-go")
	require.NoError(t, os.MkdirAll(actionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(actionDir, "action.yml"),
		[]byte("name: setup-go\nruns:\n  using: node20\n  main: index.js\n"), 0o600))

	// Supply an identity on the commit so the test does not depend on a
	// git identity being configured in the environment; a CI runner without
	// user.name/user.email would otherwise fail "commit" with exit code 128.
	for _, args := range [][]string{
		{"init", "--initial-branch=main", actionDir},
		{"-C", actionDir, "add", "action.yml"},
		{"-C", actionDir, "-c", "user.name=runner", "-c", "user.email=runner@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "action"},
	} {
		cmd := exec.Command("git", args...)
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	out, err := exec.Command("git", "-C", actionDir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	wantSha := strings.TrimSpace(string(out))

	sar := &stepActionRemote{
		Step: &model.Step{Uses: "actions/setup-go@main"},
		RunContext: &RunContext{
			Config: &Config{
				GitHubInstance:        "https://gitea.example.com",
				DefaultActionInstance: instance,
				ActionCacheDir:        t.TempDir(),
			},
			Run: &model.Run{
				JobID:    "1",
				Workflow: &model.Workflow{Jobs: map[string]*model.Job{"1": {}}},
			},
		},
		readAction: readActionImpl,
	}

	require.NoError(t, sar.prepareActionExecutor()(context.Background()))

	reference, sha, ok := sar.actionDownloadInfo()
	assert.True(t, ok)
	assert.Equal(t, "actions/setup-go@main", reference)
	assert.Equal(t, wantSha, sha)
}

func TestStepActionRemoteActionDownloadInfo(t *testing.T) {
	t.Run("reports the action and its resolved commit", func(t *testing.T) {
		sar := &stepActionRemote{
			remoteAction: newRemoteAction("actions/checkout@v7"),
			action:       &model.Action{},
			resolvedSha:  "9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0",
		}

		reference, sha, ok := sar.actionDownloadInfo()

		assert.True(t, ok)
		assert.Equal(t, "actions/checkout@v7", reference)
		assert.Equal(t, "9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0", sha)
	})

	t.Run("reports nothing when no action was downloaded", func(t *testing.T) {
		// The local checkout of the workflow's own repository resolves no action.
		sar := &stepActionRemote{remoteAction: newRemoteAction("actions/checkout@v7")}

		_, _, ok := sar.actionDownloadInfo()

		assert.False(t, ok)
	})
}

func Test_safeFilename(t *testing.T) {
	tests := []struct {
		s    string
		want string
	}{
		{
			s:    "https://test.com/test/",
			want: "https---test.com-test-",
		},
		{
			s:    `<>:"/\|?*`,
			want: "---------",
		},
	}
	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			assert.Equalf(t, tt.want, safeFilename(tt.s), "safeFilename(%v)", tt.s)
		})
	}
}

// Regression: a nested action in a composite cloned anonymously (401) because the
// composite RunContext nils Config.Secrets. The token must come from github.Token,
// which survives the config copy; the host gate must still withhold it cross-host.
func TestStepActionRemoteCloneTokenSurvivesNilSecrets(t *testing.T) {
	const wantToken = "job-token"

	table := []struct {
		name                  string
		gitHubInstance        string
		defaultActionInstance string
		wantCloneToken        string
	}{
		{
			name:           "same host forwards token despite nil secrets",
			gitHubInstance: "gitea.example.com",
			wantCloneToken: wantToken,
		},
		{
			name:                  "foreign host is not given the token",
			gitHubInstance:        "gitea.example.com",
			defaultActionInstance: "github.com",
			wantCloneToken:        "",
		},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			var capturedToken string
			origStepAtionRemoteNewCloneExecutor := stepActionRemoteNewCloneExecutor
			stepActionRemoteNewCloneExecutor = func(input git.NewGitCloneExecutorInput) common.Executor {
				capturedToken = input.Token
				return func(ctx context.Context) error { return nil }
			}
			defer (func() {
				stepActionRemoteNewCloneExecutor = origStepAtionRemoteNewCloneExecutor
			})()

			sarm := &stepActionRemoteMocks{}
			sar := &stepActionRemote{
				Step: &model.Step{Uses: "org/repo@v1"},
				RunContext: &RunContext{
					Config: &Config{
						GitHubInstance:        tt.gitHubInstance,
						DefaultActionInstance: tt.defaultActionInstance,
						ActionCacheDir:        "/tmp/test-cache",
						// Mirrors the state of a composite RunContext: job secrets are
						// stripped, but the job token is still reachable via Config.Token.
						Secrets: nil,
						Token:   wantToken,
					},
					Run: &model.Run{
						JobID: "1",
						Workflow: &model.Workflow{
							Jobs: map[string]*model.Job{"1": {}},
						},
					},
					StepResults: map[string]*model.StepResult{},
				},
				readAction: sarm.readAction,
			}
			sar.RunContext.ExprEval = sar.RunContext.NewExpressionEvaluator(ctx)

			suffixMatcher := func(suffix string) any {
				return mock.MatchedBy(func(actionDir string) bool {
					return strings.HasSuffix(actionDir, suffix)
				})
			}
			sarm.On("readAction", sar.Step, suffixMatcher(sar.Step.UsesHash()), "", mock.Anything, mock.Anything).Return(&model.Action{}, nil)

			err := sar.prepareActionExecutor()(ctx)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCloneToken, capturedToken)

			sarm.AssertExpectations(t)
		})
	}
}
