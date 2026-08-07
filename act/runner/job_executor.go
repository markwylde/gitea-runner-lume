// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/container"
	"gitea.com/gitea/runner/act/exprparser"
	"gitea.com/gitea/runner/act/model"
)

const maxJobSummaryBytes = 1024 * 1024

// jobSummaryTruncationMarker is appended to a summary that exceeded the size limit
// so the rendered output makes the truncation visible instead of silently cutting off.
const jobSummaryTruncationMarker = "\n\n---\n\n*Job summary truncated: it exceeded the maximum allowed size.*\n"

var (
	jobSummaryUploadRetryDelay = time.Second
	// jobSummaryUploadRequestTimeout bounds a single step upload request. It is kept
	// below jobSummaryUploadPhaseTimeout so one slow or unreachable request times out
	// and lets the remaining steps still upload within the phase budget, instead of a
	// single stuck request consuming the whole phase.
	jobSummaryUploadRequestTimeout = 5 * time.Second
	// jobSummaryUploadPhaseTimeout bounds the total time spent uploading all step
	// summaries. The uploads run inside the job cleanup budget that is also used to
	// stop and remove the container, so a slow or unreachable endpoint must not be
	// allowed to consume it; this keeps the remaining budget available for teardown.
	jobSummaryUploadPhaseTimeout = 15 * time.Second
)

type jobInfo interface {
	matrix() map[string]any
	steps() []*model.Step
	startContainer() common.Executor
	stopContainer() common.Executor
	closeContainer() common.Executor
	interpolateOutputs() common.Executor
	result(result string)
}

// reportStepError records a step error so the job is reported failed — except a
// cancellation, which is an interruption, not a failure.
func reportStepError(ctx context.Context, rc *RunContext, err error) {
	if errors.Is(err, context.Canceled) {
		// Defer to the job context: a genuine cancel reports cancelled, a stray teardown
		// cancellation on a live ctx is ignored — never a step FAILURE.
		rc.markInterrupted(ctx.Err())
		return
	}
	common.Logger(ctx).Errorf("##[error]%s", EscapeCommandData(err.Error()))
	common.SetJobError(ctx, err)
	rc.markFailed()
}

// actionPreparer is implemented by steps that download an action before they run, so the job
// executor can fetch all of them up front.
type actionPreparer interface {
	prepareActionExecutor() common.Executor
	actionDownloadInfo() (reference, sha string, ok bool)
}

// printPrepareActions downloads every action the job uses before its first step runs and reports
// them as actions/runner's "Prepare all required actions" section does. The steps still call
// prepareActionExecutor themselves; it is a no-op once the action is resolved here.
func printPrepareActions(rc *RunContext, preparers []actionPreparer) common.Executor {
	return func(ctx context.Context) error {
		if len(preparers) == 0 {
			return nil
		}

		rawLogger := common.Logger(ctx).WithField(rawOutputField, true)
		rawLogger.Infof("Prepare all required actions")

		for _, preparer := range preparers {
			if err := preparer.prepareActionExecutor()(ctx); err != nil {
				// No step has run yet, so the failure belongs to the job.
				reportStepError(ctx, rc, err)
				return err
			}
			reference, sha, ok := preparer.actionDownloadInfo()
			if !ok {
				continue
			}
			if sha == "" {
				rawLogger.Infof("Download action repository '%s'", reference)
			} else {
				rawLogger.Infof("Download action repository '%s' (SHA:%s)", reference, sha)
			}
		}
		return nil
	}
}

// printCompleteJobName closes the setup section the way actions/runner ends its "Set up job" step.
func printCompleteJobName(rc *RunContext) common.Executor {
	return func(ctx context.Context) error {
		// Name holds a matrix combination; JobName is the shared name GitHub reports.
		name := rc.JobName
		if name == "" {
			name = rc.Name
		}
		if name == "" && rc.Run != nil {
			name = rc.Run.JobID
		}
		common.Logger(ctx).WithField(rawOutputField, true).Infof("Complete job name: %s", name)
		return nil
	}
}

func newJobExecutor(info jobInfo, sf stepFactory, rc *RunContext) common.Executor {
	steps := make([]common.Executor, 0)
	preSteps := make([]common.Executor, 0)
	// Collected separately: every action is downloaded before the first pre step runs.
	stepPreSteps := make([]common.Executor, 0)
	preparers := make([]actionPreparer, 0)
	var postExecutor common.Executor

	steps = append(steps, func(ctx context.Context) error {
		logger := common.Logger(ctx)
		if len(info.matrix()) > 0 {
			logger.Infof("Matrix: %v", info.matrix())
		}
		return nil
	})

	infoSteps := info.steps()

	if len(infoSteps) == 0 {
		return common.NewDebugExecutor("No steps found")
	}

	preSteps = append(preSteps, func(ctx context.Context) error {
		// Have to be skipped for some Tests
		if rc.Run == nil {
			return nil
		}
		rc.ExprEval = rc.NewExpressionEvaluator(ctx)
		// evaluate environment variables since they can contain
		// GitHub's special environment variables.
		for k, v := range rc.GetEnv() {
			rc.Env[k] = rc.ExprEval.Interpolate(ctx, v)
		}
		return nil
	})

	for i, stepModel := range infoSteps {
		if stepModel == nil {
			return func(ctx context.Context) error {
				return fmt.Errorf("invalid Step %v: missing run or uses key", i)
			}
		}
		if stepModel.ID == "" {
			stepModel.ID = strconv.Itoa(i)
		}
		stepModel.Number = i

		step, err := sf.newStep(stepModel, rc)
		if err != nil {
			return common.NewErrorExecutor(err)
		}

		if preparer, ok := step.(actionPreparer); ok {
			preparers = append(preparers, preparer)
		}

		stepIdx := stepModel.Number
		preExec := step.pre()
		stepPreSteps = append(stepPreSteps, useStepLogger(rc, stepModel, stepStagePre, func(ctx context.Context) error {
			rc.CurrentStepIndex = stepIdx
			preErr := preExec(ctx)
			if preErr != nil {
				reportStepError(ctx, rc, preErr)
			} else if ctx.Err() != nil {
				reportStepError(ctx, rc, ctx.Err())
			}
			return preErr
		}))

		stepExec := step.main()
		steps = append(steps, useStepLogger(rc, stepModel, stepStageMain, func(ctx context.Context) error {
			rc.CurrentStepIndex = stepIdx
			err := stepExec(ctx)
			if err != nil {
				reportStepError(ctx, rc, err)
			} else if ctx.Err() != nil {
				reportStepError(ctx, rc, ctx.Err())
			}
			return nil
		}))

		postFn := step.post()
		postExec := useStepLogger(rc, stepModel, stepStagePost, func(ctx context.Context) error {
			rc.CurrentStepIndex = stepIdx
			err := postFn(ctx)
			if err != nil {
				reportStepError(ctx, rc, err)
			} else if ctx.Err() != nil {
				reportStepError(ctx, rc, ctx.Err())
			}
			return err
		})
		if postExecutor != nil {
			// run the post executor in reverse order
			postExecutor = postExec.Finally(postExecutor)
		} else {
			postExecutor = postExec
		}
	}

	// The setup section of the job log. The started hook goes first, so what it sets up is
	// in place for the first action download and the first step.
	preSteps = append(preSteps, rc.runJobStartedHook)
	preSteps = append(preSteps, printPrepareActions(rc, preparers))
	preSteps = append(preSteps, stepPreSteps...)
	preSteps = append(preSteps, printCompleteJobName(rc))

	// Ahead of the teardown below, while the job environment is still up.
	postExecutor = postExecutor.Finally(rc.runJobCompletedHook)

	postExecutor = postExecutor.Finally(func(ctx context.Context) error {
		jobError := common.JobError(ctx)
		var err error
		// jobError == nil keeps a failed job's container alive for post-mortem debugging when
		// AutoRemove is off (the act-CLI --rm behavior; the shipped runner always sets
		// AutoRemove). A cancelled run is not a failure to inspect, and the cancel-path post
		// context now carries its own error container so a failing post step makes jobError
		// non-nil — OR in rc.jobCancelled so cancellation still always tears the container down.
		if rc.Config.AutoRemove || jobError == nil || rc.jobCancelled {
			// always allow 1 min for stopping and removing the runner, even if we were cancelled
			ctx, cancel := context.WithTimeout(common.WithLogger(context.Background(), common.Logger(ctx)), time.Minute)
			defer cancel()

			logger := common.Logger(ctx)
			tryUploadJobSummary(ctx, rc)
			// For Gitea
			// We don't need to call `stopServiceContainers` here since it will be called by following `info.stopContainer`
			// logger.Infof("Cleaning up services for job %s", rc.JobName)
			// if err := rc.stopServiceContainers()(ctx); err != nil {
			// 	logger.Errorf("Error while cleaning services: %v", err)
			// }

			logger.Infof("Cleaning up container for job %s", rc.JobName)
			if err = info.stopContainer()(ctx); err != nil {
				logger.Errorf("##[error]%s", EscapeCommandData("Error while stop job container: "+err.Error()))
			}

			// For Gitea
			// We don't need to call `NewDockerNetworkRemoveExecutor` here since it is called by above `info.stopContainer`
			// if !rc.IsHostEnv(ctx) && rc.Config.ContainerNetworkMode == "" {
			// 	// clean network in docker mode only
			// 	// if the value of `ContainerNetworkMode` is empty string,
			// 	// it means that the network to which containers are connecting is created by `runner`,
			// 	// so, we should remove the network at last.
			// 	networkName, _ := rc.networkName()
			// 	logger.Infof("Cleaning up network for job %s, and network name is: %s", rc.JobName, networkName)
			// 	if err := container.NewDockerNetworkRemoveExecutor(networkName)(ctx); err != nil {
			// 		logger.Errorf("Error while cleaning network: %v", err)
			// 	}
			// }
		}
		setJobResult(ctx, info, rc, jobError == nil)
		setJobOutputs(ctx, rc)

		return err
	})

	stepsExecutor := newStepsExecutor(rc, preSteps, steps)

	return common.NewPipelineExecutor(info.startContainer(), stepsExecutor.
		Finally(func(ctx context.Context) error {
			// Record an interrupt (backstop for interrupts that land outside the main
			// step loop) so the post steps observe the cancelled/failed job status.
			rc.markInterrupted(ctx.Err())
			postCtx, cancel := postStepsContext(ctx)
			defer cancel()
			return postExecutor(postCtx)
		}).
		Finally(info.interpolateOutputs()).
		Finally(info.closeContainer()))
}

// postStepsContext derives the context used to run the job's post/cleanup steps from the
// finished main-pipeline context. Cleanup has to run even when the run was interrupted, so the
// returned context always carries a fresh bounded deadline and is never itself cancelled.
//
//   - context.Canceled (server cancel): detach from the cancelled context via a fresh root so
//     the post steps can run.
//   - context.DeadlineExceeded (job timeout): detach the deadline with WithoutCancel, which
//     keeps the original values — including the job-error container — so the timeout failure and
//     any post-step error are preserved and the job is still reported as failed.
//   - otherwise: run on the live context unchanged.
func postStepsContext(ctx context.Context) (context.Context, context.CancelFunc) {
	switch ctx.Err() {
	case context.Canceled:
		// The cancelled context is abandoned for a fresh root, which drops the job-error
		// container installed at the job root. Re-attach a fresh one so a failing post step
		// records its error via SetJobError instead of panicking on a nil container.
		return context.WithTimeout(common.WithJobErrorContainer(common.WithLogger(context.Background(), common.Logger(ctx))), 5*time.Minute)
	case context.DeadlineExceeded:
		return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	default:
		return ctx, func() {}
	}
}

// newStepsExecutor sequences the job's pre steps and main steps.
//
// The pre steps run as a normal pipeline that short-circuits on the first failure or
// cancellation. The main-steps executor then runs unconditionally — even if a pre step failed
// or the job was interrupted — so always()/cancelled()/failure() main steps still run, mirroring
// GitHub Actions. This is safe because each main step re-evaluates its own `if` (a pre-step
// failure flips the expression job status to failure, so success()-default steps skip) and
// newMainStepsExecutor detaches from an interrupted context before running the remaining steps.
//
// A pre-step failure or interrupt is still propagated so the job is reported with the correct
// conclusion; the pre error takes precedence since it happened first.
func newStepsExecutor(rc *RunContext, preSteps, steps []common.Executor) common.Executor {
	preExecutor := common.NewPipelineExecutor(preSteps...)
	mainExecutor := newMainStepsExecutor(rc, steps)
	return func(ctx context.Context) error {
		preErr := preExecutor(ctx)
		mainErr := mainExecutor(ctx)
		if preErr != nil {
			return preErr
		}
		return mainErr
	}
}

// newMainStepsExecutor runs the job's main-stage step executors in order. Unlike a plain
// pipeline, an interruption (context.Canceled from a server cancel, or context.DeadlineExceeded
// from the job timeout) does not abandon the remaining steps: it marks the job cancelled when
// appropriate and keeps iterating under a fresh, bounded context so steps whose `if` still
// evaluates true — always() and cancelled() — run for cleanup, mirroring GitHub Actions. Steps
// that default to success() skip themselves because success() is false once the job is no longer
// successful. The main-step wrappers report their own errors and return nil, so the loop drives
// step ordering off the context, not return values.
func newMainStepsExecutor(rc *RunContext, steps []common.Executor) common.Executor {
	return func(ctx context.Context) error {
		for i, step := range steps {
			if ctx.Err() != nil {
				return runMainStepsAfterInterrupt(ctx, rc, steps[i:])
			}
			_ = step(ctx)
		}
		// An interrupt can land during the final step, after the loop's last context
		// check; record it so the post steps still observe the cancelled/failed status.
		rc.markInterrupted(ctx.Err())
		return nil
	}
}

// runMainStepsAfterInterrupt runs the remaining main steps after the job context was cancelled or
// timed out. It detaches from the interrupted context (keeping its values: logger and job error)
// and applies a fresh deadline so always()/cancelled() steps run to completion. The original
// interrupt error is returned so callers up the chain still see the job as cancelled/timed out.
func runMainStepsAfterInterrupt(ctx context.Context, rc *RunContext, steps []common.Executor) error {
	interruptErr := ctx.Err()
	rc.markInterrupted(interruptErr)
	freshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	for _, step := range steps {
		_ = step(freshCtx)
	}
	return interruptErr
}

func setJobResult(ctx context.Context, info jobInfo, rc *RunContext, success bool) {
	logger := common.Logger(ctx)

	// Matrix combinations share one *model.Job and run in parallel; serialize the
	// read-modify-write of the job result so a failing combination is not lost-updated by a
	// concurrent succeeding one.
	job := rc.Run.Job()
	var continueOnError bool
	if !success {
		// Use a fresh context so an expired job timeout cannot block expression evaluation.
		evalCtx := common.WithLogger(context.Background(), common.Logger(ctx))
		continueOnError = evaluateJobContinueOnError(evalCtx, rc, job)
	}
	jobResult := func() string {
		defer lockJob(job)()
		result := "success"
		// we have only one result for a whole matrix build, so we need
		// to keep an existing result state if we run a matrix
		if len(info.matrix()) > 0 && job.Result != "" {
			result = job.Result
		}
		if !success {
			result = "failure"
			job.SetContinueOnError(continueOnError)
		}
		info.result(result)
		return result
	}()

	if rc.caller != nil {
		// set reusable workflow job result
		rc.caller.setReusedWorkflowJobResult(rc.JobName, jobResult) // For Gitea
		return
	}

	jobResultMessage := "succeeded"
	if jobResult != "success" {
		jobResultMessage = "failed"
	}

	logger.WithField("jobResult", jobResult).Infof("Job %s", jobResultMessage)
}

func setJobOutputs(ctx context.Context, rc *RunContext) {
	if rc.caller != nil {
		// map outputs for reusable workflows
		callerOutputs := make(map[string]string)

		ee := rc.NewExpressionEvaluator(ctx)

		for k, v := range rc.Run.Workflow.WorkflowCallConfig().Outputs {
			callerOutputs[k] = ee.Interpolate(ctx, ee.Interpolate(ctx, v.Value))
		}

		// Matrix combinations of a reusable-workflow caller share the caller's *model.Job;
		// serialize the write so parallel combos don't race on its Outputs field.
		callerJob := rc.caller.runContext.Run.Job()
		defer lockJob(callerJob)()
		callerJob.Outputs = callerOutputs
	}
}

// applyJobTimeout applies the job-level timeout-minutes to ctx, mirroring the
// step-level evaluateStepTimeout in step.go.
func applyJobTimeout(ctx context.Context, rc *RunContext, job *model.Job) (context.Context, context.CancelFunc) {
	timeout := rc.ExprEval.Interpolate(ctx, job.TimeoutMinutes)
	if timeout != "" {
		if timeoutMinutes, err := strconv.ParseInt(timeout, 10, 64); err == nil {
			return context.WithTimeout(ctx, time.Duration(timeoutMinutes)*time.Minute)
		}
	}
	return ctx, func() {}
}

// evaluateJobContinueOnError evaluates the job-level continue-on-error expression.
func evaluateJobContinueOnError(ctx context.Context, rc *RunContext, job *model.Job) bool {
	expr := strings.TrimSpace(job.RawContinueOnError)
	if expr == "" {
		return false
	}
	continueOnError, err := EvalBool(ctx, rc.NewExpressionEvaluator(ctx), expr, exprparser.DefaultStatusCheckNone)
	if err != nil {
		common.Logger(ctx).Warnf("continue-on-error expression %q evaluation failed: %v", expr, err)
		return false
	}
	return continueOnError
}

func tryUploadJobSummary(ctx context.Context, rc *RunContext) {
	if rc == nil || rc.JobContainer == nil || rc.Config == nil {
		return
	}
	// Bound the whole upload phase so a slow or unreachable endpoint cannot consume
	// the job cleanup budget reserved for stopping and removing the container.
	ctx, cancel := context.WithTimeout(ctx, jobSummaryUploadPhaseTimeout)
	defer cancel()
	env := rc.GetEnv()
	caps := strings.TrimSpace(env["GITEA_ACTIONS_CAPABILITIES"])
	if !hasJobSummaryCapability(caps) {
		// Server did not advertise support. Do not attempt upload.
		return
	}
	runtimeURL := strings.TrimSpace(env["ACTIONS_RUNTIME_URL"])
	runtimeToken := strings.TrimSpace(env["ACTIONS_RUNTIME_TOKEN"])
	runID := strings.TrimSpace(env["GITEA_RUN_ID"])
	if runtimeURL == "" || runtimeToken == "" || runID == "" {
		return
	}
	if rc.Run == nil || rc.Run.Job() == nil {
		return
	}
	// The numeric ActionRunJob ID is not exposed in the proto Task message or task context,
	// but the server signs it into the ACTIONS_RUNTIME_TOKEN JWT claims. We decode the
	// unverified claims to retrieve it; the server re-verifies the token on the request.
	jobID := extractJobIDFromRuntimeToken(runtimeToken)
	if jobID <= 0 {
		return
	}

	base := strings.TrimRight(runtimeURL, "/") + "/_apis/pipelines/workflows/" + runID +
		"/jobs/" + strconv.FormatInt(jobID, 10) + "/steps/"
	actPath := rc.JobContainer.GetActPath()
	// Reuse a single client across all step uploads so connections can be pooled.
	client := &http.Client{Timeout: jobSummaryUploadRequestTimeout}
	for i := range rc.Run.Job().Steps {
		summaryPath := path.Join(actPath, "workflow", "step-summary-"+strconv.Itoa(i)+".md")
		body, ok := readSingleFileFromContainerArchive(ctx, rc.JobContainer, summaryPath, maxJobSummaryBytes)
		if !ok || len(body) == 0 {
			continue
		}
		uploadJobSummary(ctx, client, base+strconv.Itoa(i)+"/summary", runtimeToken, body)
	}
}

// extractJobIDFromRuntimeToken returns the JobID claim from an ACTIONS_RUNTIME_TOKEN JWT
// without verifying its signature. Returns 0 if the token is unparseable or has no JobID.
func extractJobIDFromRuntimeToken(token string) int64 {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var claims struct {
		JobID int64 `json:"JobID"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return 0
	}
	return claims.JobID
}

func hasJobSummaryCapability(caps string) bool {
	return slices.Contains(strings.FieldsFunc(caps, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	}), "job-summary")
}

func uploadJobSummary(ctx context.Context, client *http.Client, url, runtimeToken string, body []byte) {
	logger := common.Logger(ctx)

	var lastStatus int
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		status, err := putJobSummary(ctx, client, url, runtimeToken, body)
		if err == nil && status/100 == 2 {
			return
		}
		lastStatus = status
		lastErr = err
		if attempt == 1 || !isTransientJobSummaryUploadFailure(status, err) {
			break
		}
		timer := time.NewTimer(jobSummaryUploadRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			lastErr = ctx.Err()
			attempt = 1
		case <-timer.C:
		}
	}

	// Best-effort only; do not fail job, but log because capability was advertised.
	if lastErr != nil {
		logger.WithError(lastErr).Warn("job summary upload failed")
		return
	}
	logger.Warnf("job summary upload failed: status=%d", lastStatus)
}

func putJobSummary(ctx context.Context, client *http.Client, url, runtimeToken string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+runtimeToken)
	req.Header.Set("Content-Type", "text/markdown; charset=utf-8")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func isTransientJobSummaryUploadFailure(status int, err error) bool {
	return err != nil || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status/100 == 5
}

func readSingleFileFromContainerArchive(ctx context.Context, env container.ExecutionsEnvironment, p string, maxBytes int64) ([]byte, bool) {
	rc, err := env.GetContainerArchive(ctx, p)
	if err != nil {
		return nil, false
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil, false
		}
		if err != nil {
			return nil, false
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if !archiveEntryMatchesPath(header.Name, p) {
			continue
		}
		// Summaries larger than the limit are truncated rather than dropped, so the
		// user still gets the leading content (mirroring how GitHub caps oversized
		// step summaries instead of discarding them). Read one extra byte so an
		// over-limit file is detected from the actual stream rather than trusting
		// header.Size, then cap the returned content at maxBytes.
		b, err := io.ReadAll(io.LimitReader(tr, maxBytes+1))
		if err != nil {
			return nil, false
		}
		if int64(len(b)) > maxBytes {
			// Reserve room for the marker so the marked-up result still fits in maxBytes.
			marker := []byte(jobSummaryTruncationMarker)
			keep := max(maxBytes-int64(len(marker)), 0)
			b = append(b[:keep], marker...)
			common.Logger(ctx).Warnf("job summary truncated: path=%s max=%d", p, maxBytes)
		}
		return b, true
	}
}

func archiveEntryMatchesPath(entryName, requestedPath string) bool {
	entryName = path.Clean(strings.TrimPrefix(entryName, "/"))
	requestedPath = path.Clean(strings.TrimPrefix(requestedPath, "/"))
	return entryName == requestedPath || entryName == path.Base(requestedPath)
}

func useStepLogger(rc *RunContext, stepModel *model.Step, stage stepStage, executor common.Executor) common.Executor {
	return func(ctx context.Context) error {
		ctx = withStepLogger(ctx, stepModel.Number, stepModel.ID, rc.ExprEval.Interpolate(ctx, stepModel.String()), stage.String())

		rawLogger := common.Logger(ctx).WithField("raw_output", true)
		logWriter := common.NewLineWriter(rc.commandHandler(ctx), func(s string) bool {
			if rc.Config.LogOutput {
				rawLogger.Infof("%s", s)
			} else {
				rawLogger.Debugf("%s", s)
			}
			return true
		})

		oldout, olderr := rc.JobContainer.ReplaceLogWriter(logWriter, logWriter)
		defer rc.JobContainer.ReplaceLogWriter(oldout, olderr)

		// Flush any buffered, not-yet-newline-terminated trailing line once the
		// step has finished, so the final line of the step's output is not lost
		// when it is not newline-terminated.
		defer common.FlushWriter(logWriter)

		return executor(ctx)
	}
}
