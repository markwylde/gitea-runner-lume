// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package poll

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"gitea.com/gitea/runner/internal/pkg/client"
	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/metrics"

	"connectrpc.com/connect"
	runnerv1 "gitea.dev/actions-proto-go/runner/v1"
	log "github.com/sirupsen/logrus"
)

// TaskRunner abstracts task execution so the poller can be tested
// without a real runner.
type TaskRunner interface {
	Run(ctx context.Context, task *runnerv1.Task) error
}

// IdleRunner can run maintenance while the poller is idle.
type IdleRunner interface {
	OnIdle(ctx context.Context)
}

// AvailabilityRunner can temporarily pause task fetching for local resource
// conditions such as low disk space without changing server-side scheduling.
type AvailabilityRunner interface {
	CanAcceptTask(ctx context.Context) (bool, string)
}

type Poller struct {
	client       client.Client
	runner       TaskRunner
	cfg          *config.Config
	tasksVersion atomic.Int64 // tasksVersion used to store the version of the last task fetched from the Gitea.

	pollingCtx      context.Context
	shutdownPolling context.CancelFunc

	jobsCtx      context.Context
	shutdownJobs context.CancelFunc

	done chan struct{}

	// unregistered is set when the server rejects the runner with an
	// Unauthenticated response, meaning the runner is no longer registered.
	unregistered       atomic.Bool
	lastHealthyPoll    atomic.Int64
	lastPollFailed     atomic.Bool
	availabilityMu     sync.Mutex
	availabilityReady  bool
	availabilityReason string
}

// workerState holds the single poller's backoff state. Consecutive empty or
// error responses drive exponential backoff; a successful task fetch resets
// both counters so the next poll fires immediately.
type workerState struct {
	consecutiveEmpty  int64
	consecutiveErrors int64
	// lastBackoff is the last interval reported to the PollBackoffSeconds gauge;
	// used to suppress redundant no-op Set calls when the backoff plateaus
	// (e.g. at FetchIntervalMax).
	lastBackoff time.Duration
}

func New(cfg *config.Config, client client.Client, runner TaskRunner) *Poller {
	pollingCtx, shutdownPolling := context.WithCancel(context.Background())

	jobsCtx, shutdownJobs := context.WithCancel(context.Background())

	done := make(chan struct{})

	p := &Poller{
		client: client,
		runner: runner,
		cfg:    cfg,

		pollingCtx:      pollingCtx,
		shutdownPolling: shutdownPolling,

		jobsCtx:      jobsCtx,
		shutdownJobs: shutdownJobs,

		done: done,
	}
	p.lastHealthyPoll.Store(time.Now().UnixNano())
	p.availabilityReady = true
	p.availabilityReason = "ok"
	return p
}

func (p *Poller) Poll() {
	sem := make(chan struct{}, p.cfg.Runner.Capacity)
	wg := &sync.WaitGroup{}
	s := &workerState{}

	defer func() {
		wg.Wait()
		close(p.done)
	}()

	for {
		select {
		case sem <- struct{}{}:
		case <-p.pollingCtx.Done():
			return
		}

		ready, reason := p.localAvailability(p.pollingCtx)
		p.reportAvailability(ready, reason)
		if !ready {
			p.runIdleMaintenance()
			<-sem
			if !p.waitBackoff(s) {
				return
			}
			continue
		}

		task, ok := p.fetchTask(p.pollingCtx, s)
		if !ok {
			p.runIdleMaintenance()
			<-sem
			if !p.waitBackoff(s) {
				return
			}
			continue
		}

		s.resetBackoff()

		wg.Add(1)
		go func(t *runnerv1.Task) {
			defer wg.Done()
			defer func() { <-sem }()
			p.runTaskWithRecover(p.jobsCtx, t)
		}(task)
	}
}

func (p *Poller) PollOnce() {
	defer close(p.done)
	s := &workerState{}
	for {
		ready, reason := p.localAvailability(p.pollingCtx)
		p.reportAvailability(ready, reason)
		if !ready {
			p.runIdleMaintenance()
			if !p.waitBackoff(s) {
				return
			}
			continue
		}
		task, ok := p.fetchTask(p.pollingCtx, s)
		if !ok {
			p.runIdleMaintenance()
			if !p.waitBackoff(s) {
				return
			}
			continue
		}
		s.resetBackoff()
		p.runTaskWithRecover(p.jobsCtx, task)
		return
	}
}

// Done returns a channel that is closed once polling has fully stopped,
// allowing callers to react when the poller shuts itself down (e.g. after the
// runner has been unregistered) rather than only on an external cancellation.
func (p *Poller) Done() <-chan struct{} {
	return p.done
}

// Unregistered reports whether polling stopped because the server rejected the
// runner as unregistered (an Unauthenticated response).
func (p *Poller) Unregistered() bool {
	return p.unregistered.Load()
}

// Ready reports whether the daemon can currently communicate with Gitea and
// accept work, reusing the availability the poll loop last observed rather than
// re-running the check. Transient transport failures are tolerated for grace.
func (p *Poller) Ready(grace time.Duration) (bool, string) {
	if p.unregistered.Load() {
		return false, "runner is no longer registered"
	}
	p.availabilityMu.Lock()
	ready, reason := p.availabilityReady, p.availabilityReason
	p.availabilityMu.Unlock()
	if !ready {
		return false, reason
	}
	if !p.lastPollFailed.Load() {
		return true, "ok"
	}
	if time.Since(time.Unix(0, p.lastHealthyPoll.Load())) <= grace {
		return true, "polling errors within grace period"
	}
	return false, "unable to poll Gitea"
}

func (p *Poller) localAvailability(ctx context.Context) (bool, string) {
	if available, ok := p.runner.(AvailabilityRunner); ok {
		return available.CanAcceptTask(ctx)
	}
	return true, "ok"
}

func (p *Poller) reportAvailability(ready bool, reason string) {
	p.availabilityMu.Lock()
	defer p.availabilityMu.Unlock()
	switch {
	case !ready && p.availabilityReady:
		log.Warnf("runner temporarily unavailable: %s", reason)
	case ready && !p.availabilityReady:
		log.Info("runner local health recovered, resuming task polling")
	}
	p.availabilityReady = ready
	p.availabilityReason = reason
}

func (p *Poller) runIdleMaintenance() {
	if idleRunner, ok := p.runner.(IdleRunner); ok {
		idleRunner.OnIdle(p.jobsCtx)
	}
}

func (p *Poller) Shutdown(ctx context.Context) error {
	p.shutdownPolling()

	select {
	// graceful shutdown completed succesfully
	case <-p.done:
		return nil

	// our timeout for shutting down ran out
	case <-ctx.Done():
		// Both the timeout and the graceful shutdown may fire
		// simultaneously. Do a non-blocking check to avoid forcing
		// a shutdown when graceful already completed.
		select {
		case <-p.done:
			return nil
		default:
		}

		// force a shutdown of all running jobs
		p.shutdownJobs()

		// wait for running jobs to report their status to Gitea
		<-p.done

		return ctx.Err()
	}
}

func (s *workerState) resetBackoff() {
	s.consecutiveEmpty = 0
	s.consecutiveErrors = 0
	s.lastBackoff = 0
}

// waitBackoff sleeps for the current backoff interval (with jitter).
// Returns false if the polling context was cancelled during the wait.
func (p *Poller) waitBackoff(s *workerState) bool {
	base := p.calculateInterval(s)
	if base != s.lastBackoff {
		metrics.PollBackoffSeconds.Set(base.Seconds())
		s.lastBackoff = base
	}
	timer := time.NewTimer(addJitter(base))
	select {
	case <-timer.C:
		return true
	case <-p.pollingCtx.Done():
		timer.Stop()
		return false
	}
}

// calculateInterval returns the polling interval with exponential backoff based on
// consecutive empty or error responses. The interval starts at FetchInterval and
// doubles with each consecutive empty/error, capped at FetchIntervalMax.
func (p *Poller) calculateInterval(s *workerState) time.Duration {
	base := p.cfg.Runner.FetchInterval
	maxInterval := p.cfg.Runner.FetchIntervalMax

	n := max(s.consecutiveEmpty, s.consecutiveErrors)
	if n <= 1 {
		return base
	}

	// Capped exponential backoff: base * 2^(n-1), max shift=5 so multiplier <= 32
	shift := min(n-1, 5)
	interval := base * time.Duration(int64(1)<<shift)
	return min(interval, maxInterval)
}

// addJitter adds +/- 20% random jitter to the given duration to avoid thundering herd.
func addJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	// jitter range: [-20%, +20%] of d
	jitterRange := int64(d) * 2 / 5 // 40% total range
	if jitterRange <= 0 {
		return d
	}
	jitter := rand.Int64N(jitterRange) - jitterRange/2
	return d + time.Duration(jitter)
}

func (p *Poller) runTaskWithRecover(ctx context.Context, task *runnerv1.Task) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("panic: %v", r)
			log.WithError(err).Error("panic in runTaskWithRecover")
		}
	}()

	if err := p.runner.Run(ctx, task); err != nil {
		log.WithError(err).Error("failed to run task")
	}
}

func (p *Poller) fetchTask(ctx context.Context, s *workerState) (*runnerv1.Task, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, p.cfg.Runner.FetchTimeout)
	defer cancel()

	// Load the version value that was in the cache when the request was sent.
	v := p.tasksVersion.Load()
	start := time.Now()
	resp, err := p.client.FetchTask(reqCtx, connect.NewRequest(&runnerv1.FetchTaskRequest{
		TasksVersion: v,
	}))

	// DeadlineExceeded is the designed idle path for a long-poll: the server
	// found no work within FetchTimeout. Treat it as an empty response and do
	// not record the duration — the timeout value would swamp the histogram.
	if errors.Is(err, context.DeadlineExceeded) {
		p.markHealthyPoll()
		s.consecutiveEmpty++
		s.consecutiveErrors = 0 // timeout is a healthy idle response
		metrics.PollFetchTotal.WithLabelValues(metrics.LabelResultEmpty).Inc()
		return nil, false
	}
	metrics.PollFetchDuration.Observe(time.Since(start).Seconds())

	if err != nil {
		// An Unauthenticated response means the server no longer knows this
		// runner (e.g. it was deleted). Retrying forever is pointless, so stop
		// polling and let the daemon exit with an error instead of spinning.
		if connect.CodeOf(err) == connect.CodeUnauthenticated {
			log.WithError(err).Error("server rejected the runner as unregistered, stopping poller")
			p.unregistered.Store(true)
			p.shutdownPolling()
			return nil, false
		}
		log.WithError(err).Error("failed to fetch task")
		p.lastPollFailed.Store(true)
		s.consecutiveErrors++
		metrics.PollFetchTotal.WithLabelValues(metrics.LabelResultError).Inc()
		metrics.ClientErrors.WithLabelValues(metrics.LabelMethodFetchTask).Inc()
		return nil, false
	}
	p.markHealthyPoll()

	// Successful response — reset error counter.
	s.consecutiveErrors = 0

	if resp == nil || resp.Msg == nil {
		s.consecutiveEmpty++
		metrics.PollFetchTotal.WithLabelValues(metrics.LabelResultEmpty).Inc()
		return nil, false
	}

	if resp.Msg.TasksVersion > v {
		p.tasksVersion.CompareAndSwap(v, resp.Msg.TasksVersion)
	}

	if resp.Msg.Task == nil {
		s.consecutiveEmpty++
		metrics.PollFetchTotal.WithLabelValues(metrics.LabelResultEmpty).Inc()
		return nil, false
	}

	// got a task, set `tasksVersion` to zero to force query db in next request.
	p.tasksVersion.CompareAndSwap(resp.Msg.TasksVersion, 0)

	metrics.PollFetchTotal.WithLabelValues(metrics.LabelResultTask).Inc()
	return resp.Msg.Task, true
}

func (p *Poller) markHealthyPoll() {
	p.lastHealthyPoll.Store(time.Now().UnixNano())
	p.lastPollFailed.Store(false)
}
