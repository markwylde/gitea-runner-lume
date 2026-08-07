// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package lock

import "sync"

// Keyed serializes the work done under one key while letting different keys run in
// parallel. Its zero value is ready to use, and it keeps one mutex per key it has seen.
type Keyed[K comparable] struct {
	mu      sync.Mutex
	mutexes map[K]*sync.Mutex
}

// Lock locks the mutex for key and returns its unlock function.
func (kl *Keyed[K]) Lock(key K) func() {
	kl.mu.Lock()
	if kl.mutexes == nil {
		kl.mutexes = map[K]*sync.Mutex{}
	}
	lock := kl.mutexes[key]
	if lock == nil {
		lock = &sync.Mutex{}
		kl.mutexes[key] = lock
	}
	kl.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}

// Delete drops the mutex for key. Holders of it keep working, they just no longer share it
// with a later Lock of the same key.
func (kl *Keyed[K]) Delete(key K) {
	kl.mu.Lock()
	delete(kl.mutexes, key)
	kl.mu.Unlock()
}
