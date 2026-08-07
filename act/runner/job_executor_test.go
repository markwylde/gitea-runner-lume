// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/container"
	"gitea.com/gitea/runner/act/model"

	log "github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v4"
)

func TestJobExecutor(t *testing.T) {
	t.Parallel()
	// Dryrun only checks syntax/planning; all cases resolve locally, so this runs offline.
	tables := []TestJobFileInfo{
		{workdir, "uses-and-run-in-one-step", "push", "Invalid run/uses syntax for job:test step:Test", platforms, secrets},
		{workdir, "uses-github-empty", "push", "Expected format {org}/{repo}[/path]@ref", platforms, secrets},
		{workdir, "uses-github-noref", "push", "Expected format {org}/{repo}[/path]@ref", platforms, secrets},
		{workdir, "uses-github-root", "push", "", platforms, secrets},
		{workdir, "uses-docker-url", "push", "", platforms, secrets},
		{workdir, "job-nil-step", "push", "invalid Step 0: missing run or uses key", platforms, secrets},
	}
	// These tests are sufficient to only check syntax.
	ctx := common.WithDryrun(context.Background(), true)
	for _, table := range tables {
		t.Run(table.workflowPath, func(t *testing.T) {
			t.Parallel()
			table.runTest(ctx, t, &Config{})
		})
	}
}

type jobInfoMock struct {
	mock.Mock
}

func (jim *jobInfoMock) matrix() map[string]any {
	args := jim.Called()
	return args.Get(0).(map[string]any)
}

func (jim *jobInfoMock) steps() []*model.Step {
	args := jim.Called()

	return args.Get(0).([]*model.Step)
}

func (jim *jobInfoMock) startContainer() common.Executor {
	args := jim.Called()

	return args.Get(0).(func(context.Context) error)
}

func (jim *jobInfoMock) stopContainer() common.Executor {
	args := jim.Called()

	return args.Get(0).(func(context.Context) error)
}

func (jim *jobInfoMock) closeContainer() common.Executor {
	args := jim.Called()

	return args.Get(0).(func(context.Context) error)
}

func (jim *jobInfoMock) interpolateOutputs() common.Executor {
	args := jim.Called()

	return args.Get(0).(func(context.Context) error)
}

func (jim *jobInfoMock) result(result string) {
	jim.Called(result)
}

type jobContainerMock struct {
	container.Container
	container.LinuxContainerEnvironmentExtensions
}

func (jcm *jobContainerMock) ReplaceLogWriter(_, _ io.Writer) (io.Writer, io.Writer) {
	return nil, nil
}

type stepFactoryMock struct {
	mock.Mock
}

func (sfm *stepFactoryMock) newStep(model *model.Step, rc *RunContext) (step, error) {
	args := sfm.Called(model, rc)
	return args.Get(0).(step), args.Error(1)
}

// actionPreparerMock stands in for a step whose action is downloaded before the job's first step.
type actionPreparerMock struct {
	reference string
	sha       string
	ok        bool
	err       error
	prepared  int
}

func (apm *actionPreparerMock) prepareActionExecutor() common.Executor {
	return func(context.Context) error {
		apm.prepared++
		return apm.err
	}
}

func (apm *actionPreparerMock) actionDownloadInfo() (string, string, bool) {
	return apm.reference, apm.sha, apm.ok
}

func TestPrintPrepareActionsGolden(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := log.New()
	logger.SetOutput(buf)
	logger.SetLevel(log.InfoLevel)
	logger.SetFormatter(&jobLogFormatter{color: cyan})
	ctx := common.WithLogger(context.Background(), logger.WithFields(log.Fields{"job": "j1"}))

	preparers := []actionPreparer{
		&actionPreparerMock{reference: "actions/checkout@v7", sha: "9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0", ok: true},
		// A resolved commit is best effort; the ref alone is reported when it is unknown.
		&actionPreparerMock{reference: "actions/setup-go@v6", ok: true},
		// A step that downloads nothing, such as the checkout of the workflow's own repository.
		&actionPreparerMock{ok: false},
	}
	require.NoError(t, printPrepareActions(&RunContext{}, preparers)(ctx))

	want := strings.Join([]string{
		"[j1]   | Prepare all required actions",
		"[j1]   | Download action repository 'actions/checkout@v7' (SHA:9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0)",
		"[j1]   | Download action repository 'actions/setup-go@v6'",
		"",
	}, "\n")
	assert.Equal(t, want, buf.String())
}

func TestPrintPrepareActionsSkipsWithoutActions(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := log.New()
	logger.SetOutput(buf)
	logger.SetFormatter(&jobLogFormatter{color: cyan})
	ctx := common.WithLogger(context.Background(), logger.WithFields(log.Fields{"job": "j1"}))

	require.NoError(t, printPrepareActions(&RunContext{}, nil)(ctx))

	assert.Empty(t, buf.String())
}

func TestPrintPrepareActionsFailsJobOnDownloadError(t *testing.T) {
	logger, _ := logrustest.NewNullLogger()
	ctx := common.WithJobErrorContainer(common.WithLogger(context.Background(), logger.WithField("job", "j1")))

	downloadErr := errors.New("failed to fetch \"actions/checkout\"")
	rc := &RunContext{}
	remaining := &actionPreparerMock{reference: "actions/setup-go@v6", ok: true}

	err := printPrepareActions(rc, []actionPreparer{
		&actionPreparerMock{err: downloadErr},
		remaining,
	})(ctx)

	require.ErrorIs(t, err, downloadErr)
	// No step has run yet, so the failure has to be recorded against the job itself.
	assert.Equal(t, downloadErr, common.JobError(ctx))
	assert.True(t, rc.jobFailed)
	assert.Zero(t, remaining.prepared)
}

func TestPrintCompleteJobName(t *testing.T) {
	for name, tt := range map[string]struct {
		rc   *RunContext
		want string
	}{
		"job name":            {rc: &RunContext{JobName: "lint", Name: "lint-1"}, want: "lint"},
		"falls back to name":  {rc: &RunContext{Name: "lint-1"}, want: "lint-1"},
		"falls back to jobID": {rc: &RunContext{Run: &model.Run{JobID: "lint"}}, want: "lint"},
	} {
		t.Run(name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			logger := log.New()
			logger.SetOutput(buf)
			logger.SetFormatter(&jobLogFormatter{color: cyan})
			ctx := common.WithLogger(context.Background(), logger.WithFields(log.Fields{"job": "j1"}))

			require.NoError(t, printCompleteJobName(tt.rc)(ctx))

			assert.Equal(t, "[j1]   | Complete job name: "+tt.want+"\n", buf.String())
		})
	}
}

// actionStepMock is a step whose action has to be downloaded before it can run.
type actionStepMock struct {
	*stepMock
	*actionPreparerMock
}

// TestNewJobExecutorDownloadsAllActionsBeforeTheFirstStep pins the shape of the setup section:
// every action is downloaded before any step runs, and the job name closes the section. A pre
// step that downloaded its own action would leave the log interleaved with the downloads.
func TestNewJobExecutorDownloadsAllActionsBeforeTheFirstStep(t *testing.T) {
	ctx := common.WithJobErrorContainer(context.Background())
	jim := &jobInfoMock{}
	sfm := &stepFactoryMock{}
	rc := &RunContext{
		JobContainer: &jobContainerMock{},
		Run: &model.Run{
			JobID: "test",
			Workflow: &model.Workflow{
				Jobs: map[string]*model.Job{"test": {}},
			},
		},
		Config: &Config{},
	}
	rc.ExprEval = rc.NewExpressionEvaluator(ctx)

	steps := []*model.Step{{ID: "1"}, {ID: "2"}}
	executorOrder := make([]string, 0)

	jim.On("steps").Return(steps)
	jim.On("matrix").Return(map[string]any{})
	jim.On("startContainer").Return(func(context.Context) error { return nil })
	jim.On("stopContainer").Return(func(context.Context) error { return nil })
	jim.On("closeContainer").Return(func(context.Context) error { return nil })
	jim.On("interpolateOutputs").Return(func(context.Context) error { return nil })
	jim.On("result", "success")

	for _, stepModel := range steps {
		sm := &stepMock{}
		apm := &actionPreparerMock{reference: "actions/checkout@v" + stepModel.ID, ok: true}
		sfm.On("newStep", stepModel, rc).Return(&actionStepMock{stepMock: sm, actionPreparerMock: apm}, nil)

		sm.On("pre").Return(func(context.Context) error {
			executorOrder = append(executorOrder, "pre"+stepModel.ID)
			return nil
		})
		sm.On("main").Return(func(context.Context) error {
			executorOrder = append(executorOrder, "step"+stepModel.ID)
			return nil
		})
		sm.On("post").Return(func(context.Context) error { return nil })

		defer sm.AssertExpectations(t)
	}

	logger, hook := logrustest.NewNullLogger()
	err := newJobExecutor(jim, sfm, rc)(common.WithLogger(ctx, logger.WithField("job", "test")))
	require.NoError(t, err)

	assert.Equal(t, []string{"pre1", "pre2", "step1", "step2"}, executorOrder)

	setup := make([]string, 0)
	for _, entry := range hook.AllEntries() {
		if strings.HasPrefix(entry.Message, "Prepare all required actions") || strings.HasPrefix(entry.Message, "Download action") ||
			strings.HasPrefix(entry.Message, "Complete job name") {
			setup = append(setup, entry.Message)
		}
	}
	assert.Equal(t, []string{
		"Prepare all required actions",
		"Download action repository 'actions/checkout@v1'",
		"Download action repository 'actions/checkout@v2'",
		"Complete job name: test",
	}, setup)
}

func TestNewJobExecutor(t *testing.T) {
	table := []struct {
		name          string
		steps         []*model.Step
		preSteps      []bool
		postSteps     []bool
		executedSteps []string
		result        string
		hasError      bool
	}{
		{
			name:          "zeroSteps",
			steps:         []*model.Step{},
			preSteps:      []bool{},
			postSteps:     []bool{},
			executedSteps: []string{},
			result:        "success",
			hasError:      false,
		},
		{
			name: "stepWithoutPrePost",
			steps: []*model.Step{{
				ID: "1",
			}},
			preSteps:  []bool{false},
			postSteps: []bool{false},
			executedSteps: []string{
				"startContainer",
				"step1",
				"stopContainer",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "success",
			hasError: false,
		},
		{
			name: "stepWithFailure",
			steps: []*model.Step{{
				ID: "1",
			}},
			preSteps:  []bool{false},
			postSteps: []bool{false},
			executedSteps: []string{
				"startContainer",
				"step1",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "failure",
			hasError: true,
		},
		{
			name: "stepWithPre",
			steps: []*model.Step{{
				ID: "1",
			}},
			preSteps:  []bool{true},
			postSteps: []bool{false},
			executedSteps: []string{
				"startContainer",
				"pre1",
				"step1",
				"stopContainer",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "success",
			hasError: false,
		},
		{
			name: "stepWithPost",
			steps: []*model.Step{{
				ID: "1",
			}},
			preSteps:  []bool{false},
			postSteps: []bool{true},
			executedSteps: []string{
				"startContainer",
				"step1",
				"post1",
				"stopContainer",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "success",
			hasError: false,
		},
		{
			name: "stepWithPreAndPost",
			steps: []*model.Step{{
				ID: "1",
			}},
			preSteps:  []bool{true},
			postSteps: []bool{true},
			executedSteps: []string{
				"startContainer",
				"pre1",
				"step1",
				"post1",
				"stopContainer",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "success",
			hasError: false,
		},
		{
			name: "stepsWithPreAndPost",
			steps: []*model.Step{{
				ID: "1",
			}, {
				ID: "2",
			}, {
				ID: "3",
			}},
			preSteps:  []bool{true, false, true},
			postSteps: []bool{false, true, true},
			executedSteps: []string{
				"startContainer",
				"pre1",
				"pre3",
				"step1",
				"step2",
				"step3",
				"post3",
				"post2",
				"stopContainer",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "success",
			hasError: false,
		},
	}

	contains := func(needle string, haystack []string) bool {
		return slices.Contains(haystack, needle)
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			fmt.Printf("::group::%s\n", tt.name) //nolint:forbidigo // pre-existing issue from nektos/act

			ctx := common.WithJobErrorContainer(context.Background())
			jim := &jobInfoMock{}
			sfm := &stepFactoryMock{}
			rc := &RunContext{
				JobContainer: &jobContainerMock{},
				Run: &model.Run{
					JobID: "test",
					Workflow: &model.Workflow{
						Jobs: map[string]*model.Job{
							"test": {},
						},
					},
				},
				Config: &Config{},
			}
			rc.ExprEval = rc.NewExpressionEvaluator(ctx)
			executorOrder := make([]string, 0)

			jim.On("steps").Return(tt.steps)

			if len(tt.steps) > 0 {
				jim.On("startContainer").Return(func(ctx context.Context) error {
					executorOrder = append(executorOrder, "startContainer")
					return nil
				})
			}

			for i, stepModel := range tt.steps {
				sm := &stepMock{}

				sfm.On("newStep", stepModel, rc).Return(sm, nil)

				sm.On("pre").Return(func(ctx context.Context) error {
					if tt.preSteps[i] {
						executorOrder = append(executorOrder, "pre"+stepModel.ID)
					}
					return nil
				})

				sm.On("main").Return(func(ctx context.Context) error {
					executorOrder = append(executorOrder, "step"+stepModel.ID)
					if tt.hasError {
						return errors.New("error")
					}
					return nil
				})

				sm.On("post").Return(func(ctx context.Context) error {
					if tt.postSteps[i] {
						executorOrder = append(executorOrder, "post"+stepModel.ID)
					}
					return nil
				})

				defer sm.AssertExpectations(t)
			}

			if len(tt.steps) > 0 {
				jim.On("matrix").Return(map[string]any{})

				jim.On("interpolateOutputs").Return(func(ctx context.Context) error {
					executorOrder = append(executorOrder, "interpolateOutputs")
					return nil
				})

				if contains("stopContainer", tt.executedSteps) {
					jim.On("stopContainer").Return(func(ctx context.Context) error {
						executorOrder = append(executorOrder, "stopContainer")
						return nil
					})
				}

				jim.On("result", tt.result)

				jim.On("closeContainer").Return(func(ctx context.Context) error {
					executorOrder = append(executorOrder, "closeContainer")
					return nil
				})
			}

			executor := newJobExecutor(jim, sfm, rc)
			err := executor(ctx)
			assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
			assert.Equal(t, tt.executedSteps, executorOrder)

			jim.AssertExpectations(t)
			sfm.AssertExpectations(t)

			fmt.Println("::endgroup::") //nolint:forbidigo // pre-existing issue from nektos/act
		})
	}
}

// TestNewJobExecutorRunsPostStepsAfterTimeout guards the timeout-minutes cleanup
// path: when a job exceeds its timeout the job context is DeadlineExceeded, but
// the post steps (cleanup hooks like actions/checkout post and cache save) must
// still run against a fresh, non-expired context, and the job must still be
// reported as failed.
func TestNewJobExecutorRunsPostStepsAfterTimeout(t *testing.T) {
	ctx := common.WithJobErrorContainer(context.Background())
	// The timeout is generous so the main step (which blocks on ctx.Done below) is
	// always reached before the deadline fires; otherwise the pipeline would
	// short-circuit before the step runs and the job error would never be set.
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	jim := &jobInfoMock{}
	sfm := &stepFactoryMock{}
	rc := &RunContext{
		JobContainer: &jobContainerMock{},
		Run: &model.Run{
			JobID: "test",
			Workflow: &model.Workflow{
				Jobs: map[string]*model.Job{
					"test": {},
				},
			},
		},
		Config: &Config{},
	}
	rc.ExprEval = rc.NewExpressionEvaluator(ctx)

	stepModel := &model.Step{ID: "1"}
	jim.On("steps").Return([]*model.Step{stepModel})
	jim.On("matrix").Return(map[string]any{})
	jim.On("startContainer").Return(func(ctx context.Context) error { return nil })
	jim.On("interpolateOutputs").Return(func(ctx context.Context) error { return nil })
	jim.On("closeContainer").Return(func(ctx context.Context) error { return nil })
	// The job timed out, so it must be reported as failed. stopContainer is left
	// unexpected on purpose: a timed-out (failed) job preserves its error state, so
	// the graceful stop is skipped exactly like any other failure without AutoRemove.
	jim.On("result", "failure")

	sm := &stepMock{}
	sfm.On("newStep", stepModel, rc).Return(sm, nil)
	sm.On("pre").Return(func(ctx context.Context) error { return nil })
	// The main step runs past the job timeout: it blocks until the job context is
	// done, mirroring a step that overruns timeout-minutes.
	sm.On("main").Return(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	var postRan bool
	var postCtxErr error
	sm.On("post").Return(func(ctx context.Context) error {
		postRan = true
		postCtxErr = ctx.Err()
		return nil
	})

	executor := newJobExecutor(jim, sfm, rc)
	// The executor itself returns nil on timeout: the failure is surfaced through
	// the job result ("failure", asserted via the result mock below), not the
	// return value.
	require.NoError(t, executor(ctx))

	assert.True(t, postRan, "post step must run after a job timeout")
	require.NoError(t, postCtxErr, "post step must run against a fresh, non-expired context")

	jim.AssertExpectations(t)
	sfm.AssertExpectations(t)
	sm.AssertExpectations(t)
}

// TestSetJobResultMatrixContinueOnError exercises the parallel-matrix path
// end-to-end: two combinations share one *model.Job and continue-on-error is
// keyed on matrix.experimental, so one combination tolerates its failure and the
// other does not. The job is reported as continue-on-error only when EVERY failing
// combination was tolerated; a single firm failure makes the whole job firm, and
// handleFailure then fails the run.
func TestSetJobResultMatrixContinueOnError(t *testing.T) {
	const jobYAML = "continue-on-error: ${{ matrix.experimental }}\nruns-on: ubuntu-latest"

	newSharedJob := func(t *testing.T) (*model.Job, *model.Workflow) {
		t.Helper()
		var job *model.Job
		require.NoError(t, yaml.Unmarshal([]byte(jobYAML), &job))
		return job, &model.Workflow{
			Name: "workflow1",
			Jobs: map[string]*model.Job{"job1": job},
		}
	}

	planFor := func(wf *model.Workflow) *model.Plan {
		return &model.Plan{Stages: []*model.Stage{{Runs: []*model.Run{{Workflow: wf, JobID: "job1"}}}}}
	}

	ctx := context.Background()

	// fail drives a single matrix combination through the failure path; each
	// RunContext is its own jobInfo (rc implements jobInfo) and shares the job.
	fail := func(wf *model.Workflow, experimental bool) {
		rc := newTestRC(wf, map[string]any{"experimental": experimental})
		setJobResult(ctx, rc, rc, false)
	}

	t.Run("one tolerated and one firm failure fails the run", func(t *testing.T) {
		job, wf := newSharedJob(t)
		// Order is intentional: the tolerated combination finishes first, then the
		// firm one. The firm-failure latch must still win regardless of order.
		fail(wf, true)
		fail(wf, false)

		assert.Equal(t, "failure", job.Result)
		assert.False(t, job.ContinueOnError, "a single firm failure must make the whole job firm")
		assert.Error(t, handleFailure(planFor(wf))(ctx))
	})

	t.Run("all tolerated failures do not fail the run", func(t *testing.T) {
		job, wf := newSharedJob(t)
		fail(wf, true)
		fail(wf, true)

		assert.Equal(t, "failure", job.Result)
		assert.True(t, job.ContinueOnError, "every failing combination was tolerated")
		assert.NoError(t, handleFailure(planFor(wf))(ctx))
	})
}

func TestHasJobSummaryCapability(t *testing.T) {
	assert.True(t, hasJobSummaryCapability("cache,job-summary artifacts"))
	assert.True(t, hasJobSummaryCapability("cache,\njob-summary\tartifacts"))
	assert.False(t, hasJobSummaryCapability("not-job-summary,job-summary-v2"))
}

// fakeRuntimeToken builds a JWT-shaped string whose middle (claims) segment encodes
// the given JobID. The header and signature segments are filler — the runner does not
// verify the signature; the server does.
func fakeRuntimeToken(jobID int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, `{"JobID":%d}`, jobID))
	sig := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return header + "." + claims + "." + sig
}

func newJobSummaryRC(env map[string]string, jobContainer container.ExecutionsEnvironment, stepCount int) *RunContext {
	steps := make([]*model.Step, stepCount)
	for i := range steps {
		steps[i] = &model.Step{ID: strconv.Itoa(i)}
	}
	return &RunContext{
		Config:       &Config{},
		JobContainer: jobContainer,
		Env:          env,
		Run: &model.Run{
			JobID: "test",
			Workflow: &model.Workflow{
				Jobs: map[string]*model.Job{
					"test": {Steps: steps},
				},
			},
		},
	}
}

func TestTryUploadJobSummaryRetriesTransientFailure(t *testing.T) {
	oldDelay := jobSummaryUploadRetryDelay
	jobSummaryUploadRetryDelay = 0
	defer func() {
		jobSummaryUploadRetryDelay = oldDelay
	}()

	runtimeToken := fakeRuntimeToken(34)

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/_apis/pipelines/workflows/12/jobs/34/steps/0/summary", r.URL.Path)
		assert.Equal(t, "Bearer "+runtimeToken, r.Header.Get("Authorization"))
		assert.Equal(t, "text/markdown; charset=utf-8", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Equal(t, []byte("# summary"), body)
		if requests == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ctx := context.Background()
	cm := &containerMock{}
	cm.On("GetContainerArchive", mock.Anything, "/var/run/act/workflow/step-summary-0.md").Return(
		io.NopCloser(bytes.NewReader(tarArchive(t, tarEntry{name: "step-summary-0.md", body: "# summary"}))),
		nil,
	).Once()

	rc := newJobSummaryRC(map[string]string{
		"GITEA_ACTIONS_CAPABILITIES": "cache, job-summary",
		"ACTIONS_RUNTIME_URL":        server.URL,
		"ACTIONS_RUNTIME_TOKEN":      runtimeToken,
		"GITEA_RUN_ID":               "12",
	}, cm, 1)

	tryUploadJobSummary(ctx, rc)

	assert.Equal(t, 2, requests)
	cm.AssertExpectations(t)
}

func TestTryUploadJobSummaryStopsAtPhaseTimeout(t *testing.T) {
	oldPhase := jobSummaryUploadPhaseTimeout
	jobSummaryUploadPhaseTimeout = 100 * time.Millisecond
	defer func() {
		jobSummaryUploadPhaseTimeout = oldPhase
	}()

	runtimeToken := fakeRuntimeToken(34)

	// The server blocks until either the request context is cancelled (the behaviour
	// under test: the phase timeout aborts the in-flight upload) or the test tears it
	// down. Without the phase timeout the upload would hang until the 30s client
	// timeout instead of releasing the cleanup budget. The release channel guarantees
	// the handler always returns so server.Close() cannot itself hang.
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	ctx := context.Background()
	cm := &containerMock{}
	cm.On("GetContainerArchive", mock.Anything, "/var/run/act/workflow/step-summary-0.md").Return(
		io.NopCloser(bytes.NewReader(tarArchive(t, tarEntry{name: "step-summary-0.md", body: "# summary"}))),
		nil,
	).Once()

	rc := newJobSummaryRC(map[string]string{
		"GITEA_ACTIONS_CAPABILITIES": "job-summary",
		"ACTIONS_RUNTIME_URL":        server.URL,
		"ACTIONS_RUNTIME_TOKEN":      runtimeToken,
		"GITEA_RUN_ID":               "12",
	}, cm, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		tryUploadJobSummary(ctx, rc)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tryUploadJobSummary did not honour the phase timeout")
	}
	cm.AssertExpectations(t)
}

func TestTryUploadJobSummaryUploadsEachStepIndependently(t *testing.T) {
	runtimeToken := fakeRuntimeToken(34)

	type upload struct {
		path string
		body string
	}
	var got []upload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		got = append(got, upload{r.URL.Path, string(body)})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ctx := context.Background()
	cm := &containerMock{}
	// Three steps: 0 has content, 1 has empty content (skipped), 2 has content.
	cm.On("GetContainerArchive", mock.Anything, "/var/run/act/workflow/step-summary-0.md").Return(
		io.NopCloser(bytes.NewReader(tarArchive(t, tarEntry{name: "step-summary-0.md", body: "first"}))),
		nil,
	).Once()
	cm.On("GetContainerArchive", mock.Anything, "/var/run/act/workflow/step-summary-1.md").Return(
		io.NopCloser(bytes.NewReader(tarArchive(t, tarEntry{name: "step-summary-1.md", body: ""}))),
		nil,
	).Once()
	cm.On("GetContainerArchive", mock.Anything, "/var/run/act/workflow/step-summary-2.md").Return(
		io.NopCloser(bytes.NewReader(tarArchive(t, tarEntry{name: "step-summary-2.md", body: "third"}))),
		nil,
	).Once()

	rc := newJobSummaryRC(map[string]string{
		"GITEA_ACTIONS_CAPABILITIES": "job-summary",
		"ACTIONS_RUNTIME_URL":        server.URL,
		"ACTIONS_RUNTIME_TOKEN":      runtimeToken,
		"GITEA_RUN_ID":               "12",
	}, cm, 3)

	tryUploadJobSummary(ctx, rc)

	assert.Equal(t, []upload{
		{"/_apis/pipelines/workflows/12/jobs/34/steps/0/summary", "first"},
		{"/_apis/pipelines/workflows/12/jobs/34/steps/2/summary", "third"},
	}, got)
	cm.AssertExpectations(t)
}

func TestTryUploadJobSummaryRequiresExactCapability(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	rc := newJobSummaryRC(map[string]string{
		"GITEA_ACTIONS_CAPABILITIES": "not-job-summary,job-summary-v2",
		"ACTIONS_RUNTIME_URL":        server.URL,
		"ACTIONS_RUNTIME_TOKEN":      fakeRuntimeToken(34),
		"GITEA_RUN_ID":               "12",
	}, &containerMock{}, 1)

	tryUploadJobSummary(context.Background(), rc)

	assert.Equal(t, 0, requests)
}

func TestTryUploadJobSummarySkipsWhenJobIDMissingFromToken(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	rc := newJobSummaryRC(map[string]string{
		"GITEA_ACTIONS_CAPABILITIES": "job-summary",
		"ACTIONS_RUNTIME_URL":        server.URL,
		"ACTIONS_RUNTIME_TOKEN":      "not-a-jwt",
		"GITEA_RUN_ID":               "12",
	}, &containerMock{}, 1)

	tryUploadJobSummary(context.Background(), rc)

	assert.Equal(t, 0, requests)
}

func TestExtractJobIDFromRuntimeToken(t *testing.T) {
	assert.Equal(t, int64(42), extractJobIDFromRuntimeToken(fakeRuntimeToken(42)))
	assert.Equal(t, int64(0), extractJobIDFromRuntimeToken("not-a-jwt"))
	assert.Equal(t, int64(0), extractJobIDFromRuntimeToken("a.b.c"))
	assert.Equal(t, int64(0), extractJobIDFromRuntimeToken(""))
}

func TestReadSingleFileFromContainerArchiveFindsMatchingRegularFile(t *testing.T) {
	ctx := context.Background()
	cm := &containerMock{}
	cm.On("GetContainerArchive", ctx, "/var/run/act/workflow/SUMMARY.md").Return(
		io.NopCloser(bytes.NewReader(tarArchive(t,
			tarEntry{name: "workflow", typeflag: tar.TypeDir},
			tarEntry{name: "other.md", body: "wrong"},
			tarEntry{name: "SUMMARY.md", body: "right"},
		))),
		nil,
	).Once()

	body, ok := readSingleFileFromContainerArchive(ctx, cm, "/var/run/act/workflow/SUMMARY.md", 1024)

	assert.True(t, ok)
	assert.Equal(t, []byte("right"), body)
	cm.AssertExpectations(t)
}

func TestReadSingleFileFromContainerArchiveTruncatesWhenTooLarge(t *testing.T) {
	logger, hook := logrustest.NewNullLogger()
	ctx := common.WithLogger(context.Background(), logger)
	cm := &containerMock{}
	content := strings.Repeat("a", 300)
	cm.On("GetContainerArchive", ctx, "/var/run/act/workflow/SUMMARY.md").Return(
		io.NopCloser(bytes.NewReader(tarArchive(t, tarEntry{name: "SUMMARY.md", body: content}))),
		nil,
	).Once()

	const maxBytes = 200
	body, ok := readSingleFileFromContainerArchive(ctx, cm, "/var/run/act/workflow/SUMMARY.md", maxBytes)

	// Oversized summaries are truncated to the limit (reserving room for the marker)
	// rather than dropped entirely, and the truncation marker is appended.
	assert.True(t, ok)
	assert.LessOrEqual(t, len(body), maxBytes)
	keep := maxBytes - len(jobSummaryTruncationMarker)
	assert.Equal(t, []byte(content[:keep]+jobSummaryTruncationMarker), body)
	if assert.Len(t, hook.Entries, 1) {
		assert.Contains(t, hook.Entries[0].Message, "job summary truncated")
	}
	cm.AssertExpectations(t)
}

func TestReadSingleFileFromContainerArchiveKeepsExactLimitWithoutWarning(t *testing.T) {
	logger, hook := logrustest.NewNullLogger()
	ctx := common.WithLogger(context.Background(), logger)
	cm := &containerMock{}
	cm.On("GetContainerArchive", ctx, "/var/run/act/workflow/SUMMARY.md").Return(
		io.NopCloser(bytes.NewReader(tarArchive(t, tarEntry{name: "SUMMARY.md", body: "abc"}))),
		nil,
	).Once()

	body, ok := readSingleFileFromContainerArchive(ctx, cm, "/var/run/act/workflow/SUMMARY.md", 3)

	// A summary that is exactly at the limit is kept whole and not flagged as truncated.
	assert.True(t, ok)
	assert.Equal(t, []byte("abc"), body)
	assert.Empty(t, hook.Entries)
	cm.AssertExpectations(t)
}

type tarEntry struct {
	name     string
	body     string
	typeflag byte
}

func tarArchive(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()

	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{
			Name:     entry.name,
			Typeflag: typeflag,
			Mode:     0o644,
			Size:     int64(len(entry.body)),
		}
		if typeflag == tar.TypeDir {
			header.Mode = 0o755
			header.Size = 0
		}
		require.NoError(t, tw.WriteHeader(header))
		if typeflag == tar.TypeReg {
			_, err := tw.Write([]byte(entry.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func newTestRC(wf *model.Workflow, matrix map[string]any) *RunContext {
	return &RunContext{
		Config: &Config{
			Workdir: ".",
			Platforms: map[string]string{
				"ubuntu-latest": "ubuntu-latest",
			},
		},
		StepResults: map[string]*model.StepResult{},
		Env:         map[string]string{},
		Matrix:      matrix,
		Run:         &model.Run{JobID: "job1", Workflow: wf},
	}
}

func makeTestRC(t *testing.T, jobYAML string) *RunContext {
	t.Helper()
	var job *model.Job
	require.NoError(t, yaml.Unmarshal([]byte(jobYAML), &job))
	rc := newTestRC(&model.Workflow{
		Name: "workflow1",
		Jobs: map[string]*model.Job{"job1": job},
	}, nil)
	rc.ExprEval = rc.NewExpressionEvaluator(context.Background())
	return rc
}

func TestApplyJobTimeout(t *testing.T) {
	cases := []struct {
		name        string
		yaml        string
		wantTimeout bool
	}{
		{"empty", "runs-on: ubuntu-latest", false},
		{"integer", "timeout-minutes: 5\nruns-on: ubuntu-latest", true},
		{"non-numeric ignored", "timeout-minutes: abc\nruns-on: ubuntu-latest", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := makeTestRC(t, tc.yaml)
			ctx := context.Background()
			newCtx, cancel := applyJobTimeout(ctx, rc, rc.Run.Job())
			defer cancel()
			_, hasDeadline := newCtx.Deadline()
			assert.Equal(t, tc.wantTimeout, hasDeadline)
		})
	}
}

func TestEvaluateJobContinueOnError(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want bool
	}{
		{"absent", "runs-on: ubuntu-latest", false},
		{"true", "continue-on-error: true\nruns-on: ubuntu-latest", true},
		{"false", "continue-on-error: false\nruns-on: ubuntu-latest", false},
		{"expression true", "continue-on-error: ${{ 'x' == 'x' }}\nruns-on: ubuntu-latest", true},
		{"expression false", "continue-on-error: ${{ 'x' != 'x' }}\nruns-on: ubuntu-latest", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := makeTestRC(t, tc.yaml)
			got := evaluateJobContinueOnError(context.Background(), rc, rc.Run.Job())
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestJobSetContinueOnError(t *testing.T) {
	t.Run("first call true", func(t *testing.T) {
		j := &model.Job{}
		j.SetContinueOnError(true)
		assert.True(t, j.ContinueOnError)
	})
	t.Run("first call false", func(t *testing.T) {
		j := &model.Job{}
		j.SetContinueOnError(false)
		assert.False(t, j.ContinueOnError)
	})
	t.Run("true then false locks to false", func(t *testing.T) {
		j := &model.Job{}
		j.SetContinueOnError(true)
		j.SetContinueOnError(false)
		assert.False(t, j.ContinueOnError)
	})
	t.Run("false then true stays false", func(t *testing.T) {
		j := &model.Job{}
		j.SetContinueOnError(false)
		j.SetContinueOnError(true)
		assert.False(t, j.ContinueOnError)
	})
	t.Run("true then true stays true", func(t *testing.T) {
		j := &model.Job{}
		j.SetContinueOnError(true)
		j.SetContinueOnError(true)
		assert.True(t, j.ContinueOnError)
	})
}
