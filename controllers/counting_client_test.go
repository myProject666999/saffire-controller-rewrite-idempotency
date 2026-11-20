/*
Copyright 2020 Fairwinds

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License
*/

package controllers

import (
	"context"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CallCounters separately records the write call types a reconcile can make.
// Status writes and object writes must be asserted separately because a status
// update does not trigger a rollout even though it bumps resourceVersion.
type CallCounters struct {
	Updates       int
	Patches       int
	StatusUpdates int
	StatusPatches int
	Creates       int
	Deletes       int
}

// CountingClient wraps a controller-runtime client and counts write calls.
type CountingClient struct {
	client.Client
	mu    sync.Mutex
	calls CallCounters
	// PatchHook runs after every successful Patch. Tests use it to emulate an
	// external syncer that immediately reverts the template image.
	PatchHook func(obj client.Object)
}

// Snapshot returns a copy of the counters safe to read after the fact.
func (c *CountingClient) Snapshot() CallCounters {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// Reset zeroes all counters.
func (c *CountingClient) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = CallCounters{}
}

// Update counts object updates (the reconciler is expected never to use these
// on workloads; it patches instead).
func (c *CountingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.mu.Lock()
	c.calls.Updates++
	c.mu.Unlock()
	return c.Client.Update(ctx, obj, opts...)
}

// Patch counts strategic-merge patches on workload objects or standalone pods.
func (c *CountingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.mu.Lock()
	c.calls.Patches++
	c.mu.Unlock()
	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}
	if c.PatchHook != nil {
		c.PatchHook(obj)
	}
	return nil
}

// Create counts object creates.
func (c *CountingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.mu.Lock()
	c.calls.Creates++
	c.mu.Unlock()
	return c.Client.Create(ctx, obj, opts...)
}

// Delete counts object deletes.
func (c *CountingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.mu.Lock()
	c.calls.Deletes++
	c.mu.Unlock()
	return c.Client.Delete(ctx, obj, opts...)
}

// Status returns a counting sub-resource writer.
func (c *CountingClient) Status() client.SubResourceWriter {
	return &countingStatusWriter{inner: c.Client.Status(), parent: c}
}

type countingStatusWriter struct {
	inner  client.SubResourceWriter
	parent *CountingClient
}

func (s *countingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	s.parent.mu.Lock()
	s.parent.calls.StatusUpdates++
	s.parent.mu.Unlock()
	return s.inner.Update(ctx, obj, opts...)
}

func (s *countingStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	s.parent.mu.Lock()
	s.parent.calls.StatusPatches++
	s.parent.mu.Unlock()
	return s.inner.Patch(ctx, obj, patch, opts...)
}

func (s *countingStatusWriter) Create(ctx context.Context, obj, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return s.inner.Create(ctx, obj, subResource, opts...)
}
