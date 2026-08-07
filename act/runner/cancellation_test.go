// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/exprparser"
	"gitea.com/gitea/runner/act/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
)

// TestCancelledJobStatusEnablesAlwaysAndCancelledSteps verifies that once a job is
// cancelled, getJobContext reports the "cancelled" status so the step `if` functions
// evaluate the way GitHub Actions does: cancelled()/always() are true, success()/failure()
// are false. A step that defaults to success() is therefore skipped while an always() step
// still runs. Before the fix the status could only ever be success/failure, so cancelled()
// was structurally impossible and cancel-only cleanup steps never ran.
func TestCancelledJobStatusEnablesAlwaysAndCancelledSteps(t *testing.T) {
	rc := createIfTestRunContext(map[string]*model.Job{
		"job1": createJob(t, `runs-on: ubuntu-latest`, ""),
	})
	rc.markCancelled()

	// The core fix: the job status context now reports "cancelled" instead of being
	// pinned to success/failure.
	jobCtx := rc.getJobContext()
	require.Equal(t, "cancelled", jobCtx.Status)

	// Feed that status through the step-context expression functions, which is what a
	// step `if` evaluates. On a cancelled job only always()/cancelled() are true.
	interp := exprparser.NewInterpeter(
		&exprparser.EvaluationEnvironment{Job: jobCtx},
		exprparser.Config{Context: "step"},
	)
	for expr, want := range map[string]bool{
		"cancelled()":  true,
		"always()":     true,
		"success()":    false,
		"failure()":    false,
		"!cancelled()": false,
	} {
		got, err := interp.Evaluate(expr, exprparser.DefaultStatusCheckNone)
		require.NoErrorf(t, err, "Evaluate(%q)", expr)
		assert.Equalf(t, want, got, "Evaluate(%q) on a cancelled job", expr)
	}

	// A step without an `if` defaults to success() and must be skipped on cancel,
	// while an `if: always()` step must still run.
	disabled, err := interp.Evaluate("", exprparser.DefaultStatusCheckSuccess)
	require.NoError(t, err)
	assert.Equal(t, false, disabled, "default-success step must be skipped on a cancelled job")

	enabled, err := interp.Evaluate("always()", exprparser.DefaultStatusCheckSuccess)
	require.NoError(t, err)
	assert.Equal(t, true, enabled, "`if: always()` step must run on a cancelled job")
}

// TestMainStepsExecutorRunsAlwaysStepsAfterCancel verifies that newMainStepsExecutor does
// not abandon the remaining steps when the run is cancelled mid-pipeline. The later step
// still runs (so a main-stage always() step is reached), it runs under a fresh,
// non-cancelled context, and the job is marked cancelled. The interrupt error is still
// propagated so callers up the chain see the cancellation.
func TestMainStepsExecutorRunsAlwaysStepsAfterCancel(t *testing.T) {
	rc := createIfTestRunContext(map[string]*model.Job{
		"job1": createJob(t, `runs-on: ubuntu-latest`, ""),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ran []string
	var laterStepCtxErr error
	steps := []common.Executor{
		func(_ context.Context) error {
			ran = append(ran, "step1")
			cancel() // server cancellation lands while step1 runs
			return nil
		},
		func(c context.Context) error {
			ran = append(ran, "always-step")
			laterStepCtxErr = c.Err()
			return nil
		},
	}

	err := newMainStepsExecutor(rc, steps)(ctx)

	require.ErrorIs(t, err, context.Canceled, "interrupt error is propagated")
	assert.Equal(t, []string{"step1", "always-step"}, ran, "the always() step still runs after cancel")
	require.NoError(t, laterStepCtxErr, "remaining steps run under a fresh, non-cancelled context")
	assert.True(t, rc.jobCancelled, "the job is marked cancelled")
}

// TestMainStepsExecutorMarksFailedOnTimeoutBetweenSteps guards the timeout path's symmetry with the cancel path.
// When the job deadline (timeout-minutes) lands in the gap between two steps, the job must be marked as failed (not cancelled),
// so always()/failure() cleanup steps run while default success() steps skip, and so the timed-out job is not reported as success.
func TestMainStepsExecutorMarksFailedOnTimeoutBetweenSteps(t *testing.T) {
	rc := createIfTestRunContext(map[string]*model.Job{
		"job1": createJob(t, `runs-on: ubuntu-latest`, ""),
	})

	// A short deadline that we let elapse between steps, so no step records the error itself.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	var ran []string
	var laterStepCtxErr error
	steps := []common.Executor{
		func(c context.Context) error {
			ran = append(ran, "step1")
			// Block until the job deadline elapses, then return cleanly: the interrupt lands in the loop's between-steps check, not inside a step.
			<-c.Done()
			return nil
		},
		func(c context.Context) error {
			ran = append(ran, "always-step")
			laterStepCtxErr = c.Err()
			return nil
		},
	}

	err := newMainStepsExecutor(rc, steps)(ctx)

	require.ErrorIs(t, err, context.DeadlineExceeded, "the timeout error is propagated")
	assert.Equal(t, []string{"step1", "always-step"}, ran, "the always() step still runs after a timeout")
	require.NoError(t, laterStepCtxErr, "remaining steps run under a fresh, non-expired context")
	assert.True(t, rc.jobFailed, "a job timeout marks the job failed")
	assert.False(t, rc.jobCancelled, "a timeout is not a cancellation")

	// The status the real main-step `if` evaluation sees: "failure", so default success() steps skip while always()/failure() steps run.
	assert.Equal(t, "failure", rc.getJobContext().Status)
}

// TestStepsExecutorRunsMainStepsAfterPreCancel verifies that a cancellation landing during the
// pre phase does not abandon the main steps: newStepsExecutor still runs the main-steps executor,
// so a main-stage always()/cancelled() step is reached (under a fresh, non-cancelled context),
// the job is marked cancelled, and the cancellation is propagated. Before the fix the `.Then(...)`
// short-circuit skipped the main steps entirely when a pre step was cancelled.
func TestStepsExecutorRunsMainStepsAfterPreCancel(t *testing.T) {
	rc := createIfTestRunContext(map[string]*model.Job{
		"job1": createJob(t, `runs-on: ubuntu-latest`, ""),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ran []string
	var mainStepCtxErr error
	preSteps := []common.Executor{
		func(_ context.Context) error {
			ran = append(ran, "pre1")
			cancel() // server cancellation lands during the pre phase
			return nil
		},
	}
	steps := []common.Executor{
		func(c context.Context) error {
			ran = append(ran, "always-step")
			mainStepCtxErr = c.Err()
			return nil
		},
	}

	err := newStepsExecutor(rc, preSteps, steps)(ctx)

	require.ErrorIs(t, err, context.Canceled, "the cancellation is propagated")
	assert.Equal(t, []string{"pre1", "always-step"}, ran, "the main always() step runs after a pre-phase cancel")
	require.NoError(t, mainStepCtxErr, "the main step runs under a fresh, non-cancelled context")
	assert.True(t, rc.jobCancelled, "the job is marked cancelled")
}

// TestStepsExecutorRunsMainStepsAfterPreFailure verifies that a failing pre step does not abandon
// the main steps: they still run (so a main-stage always()/failure() step is reached), and the
// pre-step error is propagated so the job is reported as failed. The main steps' own `if`
// evaluation is what skips success()-default steps, so running them here is safe.
func TestStepsExecutorRunsMainStepsAfterPreFailure(t *testing.T) {
	rc := createIfTestRunContext(map[string]*model.Job{
		"job1": createJob(t, `runs-on: ubuntu-latest`, ""),
	})

	var ran []string
	preSteps := []common.Executor{
		func(_ context.Context) error {
			ran = append(ran, "pre1")
			return assert.AnError
		},
	}
	steps := []common.Executor{
		func(_ context.Context) error {
			ran = append(ran, "always-step")
			return nil
		},
	}

	err := newStepsExecutor(rc, preSteps, steps)(context.Background())

	require.ErrorIs(t, err, assert.AnError, "the pre-step error is propagated")
	assert.Equal(t, []string{"pre1", "always-step"}, ran, "the main always() step runs after a pre-step failure")
	assert.False(t, rc.jobCancelled, "a pre-step failure is not a cancellation")
}

// TestPreStepFailureAffectsMainStepIfStatus verifies the status path used by real
// main-step `if` evaluation. A pre-step failure is not present in StepResults, so
// recording only the context job error is not enough: getJobContext must also report
// failure so success()-default main steps skip and failure() steps run.
func TestPreStepFailureAffectsMainStepIfStatus(t *testing.T) {
	rc := createIfTestRunContext(map[string]*model.Job{
		"job1": createJob(t, `runs-on: ubuntu-latest`, ""),
	})
	ctx := common.WithJobErrorContainer(context.Background())

	reportStepError(ctx, rc, assert.AnError)

	assert.Equal(t, "failure", rc.getJobContext().Status)
	require.ErrorIs(t, common.JobError(ctx), assert.AnError)

	defaultStep := &stepRun{
		RunContext: rc,
		Step:       &model.Step{ID: "default-step"},
		env:        map[string]string{},
	}
	defaultEnabled, err := isStepEnabled(ctx, defaultStep.getIfExpression(ctx, stepStageMain), defaultStep, stepStageMain)
	require.NoError(t, err)
	assert.False(t, defaultEnabled, "default success() main step must skip after a pre-step failure")

	failureStep := &stepRun{
		RunContext: rc,
		Step: &model.Step{
			ID: "failure-step",
			If: yaml.Node{Value: "failure()"},
		},
		env: map[string]string{},
	}
	failureEnabled, err := isStepEnabled(ctx, failureStep.getIfExpression(ctx, stepStageMain), failureStep, stepStageMain)
	require.NoError(t, err)
	assert.True(t, failureEnabled, "failure() main step must run after a pre-step failure")
}

// TestPostStepsContextCancelledIsUsableForFailingStep guards against a panic: post/cleanup
// steps run on a context derived from the cancelled job context, and a failing post step
// records its error via common.SetJobError. If that derived context lacks a job-error container,
// SetJobError dereferences a nil map and panics. The post context must therefore be detached
// from cancellation (so the steps run) yet still carry a usable error container.
func TestPostStepsContextCancelledIsUsableForFailingStep(t *testing.T) {
	cancelled, cancel := context.WithCancel(common.WithJobErrorContainer(context.Background()))
	cancel()
	require.ErrorIs(t, cancelled.Err(), context.Canceled)

	postCtx, done := postStepsContext(cancelled)
	defer done()

	// Detached from cancellation, so the post steps actually run.
	require.NoError(t, postCtx.Err(), "post context must not be cancelled")

	// A failing post step records its error instead of panicking.
	require.NotPanics(t, func() {
		common.SetJobError(postCtx, assert.AnError)
	}, "a failing post step must not panic on the cancel path")
	assert.ErrorIs(t, common.JobError(postCtx), assert.AnError)
}

// TestPostStepsContextDeadlinePreservesJobError verifies the job-timeout path keeps the original
// job-error container (via context.WithoutCancel), so the timeout failure and any post-step error
// survive into the post phase and the job is still reported as failed.
func TestPostStepsContextDeadlinePreservesJobError(t *testing.T) {
	base := common.WithJobErrorContainer(context.Background())
	common.SetJobError(base, assert.AnError)
	expired, cancel := context.WithDeadline(base, time.Now().Add(-time.Hour))
	defer cancel()
	require.ErrorIs(t, expired.Err(), context.DeadlineExceeded)

	postCtx, done := postStepsContext(expired)
	defer done()

	require.NoError(t, postCtx.Err(), "post context must not carry the expired deadline")
	assert.ErrorIs(t, common.JobError(postCtx), assert.AnError, "the timeout job error must be preserved")
}

// reportStepError must treat a context.Canceled (e.g. a teardown-cancelled read) as an
// interruption, never a job failure.
func TestReportStepErrorTreatsCancelAsInterruption(t *testing.T) {
	rc := &RunContext{}

	// stray read cancellation while the job context is live: ignored, not a failure
	live := common.WithJobErrorContainer(context.Background())
	reportStepError(live, rc, context.Canceled)
	require.NoError(t, common.JobError(live))
	assert.False(t, rc.jobFailed)
	assert.False(t, rc.jobCancelled)

	// genuine job cancellation: recorded as cancelled, still not a failure
	cancelled, cancel := context.WithCancel(common.WithJobErrorContainer(context.Background()))
	cancel()
	reportStepError(cancelled, rc, context.Canceled)
	require.NoError(t, common.JobError(cancelled))
	assert.False(t, rc.jobFailed)
	assert.True(t, rc.jobCancelled)

	// a real error still fails the job
	failed := common.WithJobErrorContainer(context.Background())
	reportStepError(failed, rc, assert.AnError)
	require.ErrorIs(t, common.JobError(failed), assert.AnError)
	assert.True(t, rc.jobFailed)
}
