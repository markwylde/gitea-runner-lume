// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"context"
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestDryrunContext(t *testing.T) {
	ctx := context.Background()
	if Dryrun(ctx) {
		t.Fatal("plain context should not be dryrun")
	}
	if !Dryrun(WithDryrun(ctx, true)) {
		t.Fatal("WithDryrun(true) should set dryrun")
	}
	if Dryrun(WithDryrun(ctx, false)) {
		t.Fatal("WithDryrun(false) should clear dryrun")
	}
}

func TestJobErrorContainer(t *testing.T) {
	ctx := context.Background()
	err := errors.New("job failed")

	SetJobError(ctx, err)
	if got := JobError(ctx); got != nil {
		t.Fatalf("JobError without container = %v, want nil", got)
	}

	ctx = WithJobErrorContainer(ctx)
	SetJobError(ctx, err)
	if got := JobError(ctx); !errors.Is(got, err) {
		t.Fatalf("JobError = %v, want %v", got, err)
	}
}

func TestLoggerAndHookContext(t *testing.T) {
	ctx := context.Background()
	if Logger(ctx) != logrus.StandardLogger() {
		t.Fatal("plain context should use standard logger")
	}
	if LoggerHook(ctx) != nil {
		t.Fatal("plain context should not have a logger hook")
	}

	logger := logrus.New()
	ctx = WithLogger(ctx, logger)
	if Logger(ctx) != logger {
		t.Fatal("WithLogger should set logger")
	}

	hook := testHook{}
	ctx = WithLoggerHook(ctx, hook)
	if LoggerHook(ctx) != hook {
		t.Fatal("WithLoggerHook should set hook")
	}
}

type testHook struct{}

func (testHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (testHook) Fire(*logrus.Entry) error {
	return nil
}
