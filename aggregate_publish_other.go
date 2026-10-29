//go:build !linux

package main

import (
	"fmt"
	"os"
)

func publishAggregateDirectory(_, _ string) error {
	return fmt.Errorf("combined output publication requires Linux atomic no-replace directory rename")
}

func acquireAggregateStagingLock(string) (*os.File, error) {
	return nil, fmt.Errorf("combined output publication requires Linux advisory file locks")
}

func tryLockAggregateStaging(string) (*os.File, bool, error) {
	return nil, false, fmt.Errorf("combined output publication requires Linux advisory file locks")
}

func releaseAggregateStagingLock(*os.File) {}
