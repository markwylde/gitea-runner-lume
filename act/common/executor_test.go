// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWorkflow(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()

	// empty
	emptyWorkflow := NewPipelineExecutor()
	assert.NoError(emptyWorkflow(ctx)) //nolint:testifylint // pre-existing issue from nektos/act

	// error case
	errorWorkflow := NewErrorExecutor(errors.New("test error"))
	assert.Error(errorWorkflow(ctx)) //nolint:testifylint // pre-existing issue from nektos/act

	// multiple success case
	runcount := 0
	successWorkflow := NewPipelineExecutor(
		func(ctx context.Context) error {
			runcount++
			return nil
		},
		func(ctx context.Context) error {
			runcount++
			return nil
		})
	assert.NoError(successWorkflow(ctx)) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(2, runcount)
}

func TestNewConditionalExecutor(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()

	trueCount := 0
	falseCount := 0

	err := NewConditionalExecutor(func(ctx context.Context) bool {
		return false
	}, func(ctx context.Context) error {
		trueCount++
		return nil
	}, func(ctx context.Context) error {
		falseCount++
		return nil
	})(ctx)

	assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(0, trueCount)
	assert.Equal(1, falseCount)

	err = NewConditionalExecutor(func(ctx context.Context) bool {
		return true
	}, func(ctx context.Context) error {
		trueCount++
		return nil
	}, func(ctx context.Context) error {
		falseCount++
		return nil
	})(ctx)

	assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(1, trueCount)
	assert.Equal(1, falseCount)
}

// concurrencyProbe returns an executor recording the peak number of concurrent copies. Copies
// block until wantActive are in flight so the peak is exact without sleeping, and later copies
// find the gate already open so the last one still finishes with no partner left.
func concurrencyProbe(wantActive int32) (exec Executor, count, maxActive *atomic.Int32) {
	var counted, active, peak atomic.Int32
	var once sync.Once
	reached := make(chan struct{})

	return func(ctx context.Context) error {
		counted.Add(1)
		running := active.Add(1)
		for {
			seen := peak.Load()
			if running <= seen || peak.CompareAndSwap(seen, running) {
				break
			}
		}
		if running >= wantActive {
			once.Do(func() { close(reached) })
		}
		<-reached
		active.Add(-1)
		return nil
	}, &counted, &peak
}

func TestNewParallelExecutor(t *testing.T) {
	ctx := context.Background()

	exec, count, maxActive := concurrencyProbe(2)
	require.NoError(t, NewParallelExecutor(2, exec, exec, exec)(ctx))
	assert.Equal(t, int32(3), count.Load(), "should run all 3 executors")
	assert.Equal(t, int32(2), maxActive.Load(), "should run at most 2 executors in parallel")

	// parallelism below 1 falls back to a single worker
	exec, count, maxActive = concurrencyProbe(1)
	require.NoError(t, NewParallelExecutor(0, exec, exec, exec)(ctx))
	assert.Equal(t, int32(3), count.Load(), "should run all 3 executors")
	assert.Equal(t, int32(1), maxActive.Load(), "should run at most 1 executor in parallel")
}

func TestNewParallelExecutorEmpty(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()
	require.NoError(t, NewParallelExecutor(2)(ctx))

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewParallelExecutor(2)(canceledCtx)
	assert.ErrorIs(err, context.Canceled)
}

func TestNewParallelExecutorFailed(t *testing.T) {
	assert := assert.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	count := 0
	errorWorkflow := NewPipelineExecutor(func(ctx context.Context) error {
		count++
		return errors.New("fake error")
	})
	err := NewParallelExecutor(1, errorWorkflow)(ctx)
	assert.Equal(1, count)
	assert.ErrorIs(context.Canceled, err)
}

func TestNewParallelExecutorCanceled(t *testing.T) {
	assert := assert.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	errExpected := errors.New("fake error")

	var count atomic.Int32
	successWorkflow := NewPipelineExecutor(func(ctx context.Context) error {
		count.Add(1)
		return nil
	})
	errorWorkflow := NewPipelineExecutor(func(ctx context.Context) error {
		count.Add(1)
		return errExpected
	})
	err := NewParallelExecutor(3, errorWorkflow, successWorkflow, successWorkflow)(ctx)
	assert.Equal(int32(3), count.Load())
	assert.Error(errExpected, err) //nolint:testifylint // pre-existing issue from nektos/act
}

func TestNewParallelExecutorRunsRemainingAfterFailure(t *testing.T) {
	var successCount atomic.Int32
	executors := make([]Executor, 5)
	for i := range executors {
		executors[i] = func(ctx context.Context) error {
			if i == 2 {
				return errors.New("fake error")
			}
			successCount.Add(1)
			return nil
		}
	}

	require.Error(t, NewParallelExecutor(2, executors...)(context.Background()))
	assert.Equal(t, int32(4), successCount.Load(), "a failing executor must not stop the others")
}

func TestExecutorConditionalsAndFinally(t *testing.T) {
	ctx := context.Background()
	var calls []string
	record := func(name string) Executor {
		return func(ctx context.Context) error {
			calls = append(calls, name)
			return nil
		}
	}

	require.NoError(t, record("if-true").If(func(context.Context) bool { return true })(ctx))
	require.NoError(t, record("if-false").If(func(context.Context) bool { return false })(ctx))
	require.NoError(t, record("if-not").IfNot(func(context.Context) bool { return false })(ctx))
	require.NoError(t, record("if-bool").IfBool(true)(ctx))
	require.NoError(t, record("main").Finally(record("finally"))(ctx))

	want := []string{"if-true", "if-not", "if-bool", "main", "finally"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestExecutorFinallyReturnsFinallyErrorWithOriginal(t *testing.T) {
	mainErr := errors.New("main failed")
	finalErr := errors.New("cleanup failed")

	err := NewErrorExecutor(mainErr).Finally(NewErrorExecutor(finalErr))(context.Background())
	require.Error(t, err)
	if !strings.Contains(err.Error(), "cleanup failed") || !strings.Contains(err.Error(), "main failed") {
		t.Fatalf("finally error = %q, want both cleanup and original error", err)
	}
}

func TestConditionalNot(t *testing.T) {
	cond := Conditional(func(context.Context) bool { return false })
	if !cond.Not()(context.Background()) {
		t.Fatal("inverted conditional should be true")
	}
}
