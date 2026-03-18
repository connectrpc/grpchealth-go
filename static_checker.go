// Copyright 2022-2024 The Connect Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package grpchealth

import (
	"context"
	"fmt"
	"sync"

	"connectrpc.com/connect"
)

// StaticChecker is a simple Checker implementation. It always returns
// StatusServing for the process, and it returns a static value for each
// service. It also implements [Watcher], so the handler returned by
// [NewHandler] supports the streaming Watch RPC.
//
// If you have a dynamic list of services, want to ping a database as part of
// your health check, or otherwise need something more specialized, you should
// write a custom Checker implementation.
type StaticChecker struct {
	mu       sync.RWMutex
	statuses map[string]Status
	watchers map[string][]*staticWatchEntry
}

type staticWatchEntry struct {
	onChange func()
}

// NewStaticChecker constructs a StaticChecker. By default, each of the
// supplied services has StatusServing.
//
// The supplied strings should be fully-qualified protobuf service names (for
// example, "acme.user.v1.UserService"). Generated Connect service files
// have this declared as a constant.
func NewStaticChecker(services ...string) *StaticChecker {
	statuses := make(map[string]Status, len(services))
	for _, service := range services {
		statuses[service] = StatusServing
	}
	return &StaticChecker{statuses: statuses}
}

// SetStatus sets the health status of a service, registering a new service if
// necessary. It's safe to call SetStatus and Check concurrently.
//
// If the given service name is empty, it sets a server-wide status that is
// returned to check requests that do not request a particular service. If no
// such status is ever set, checks that do not request a particular service
// will get a response of StatusServing.
func (c *StaticChecker) SetStatus(service string, status Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses[service] = status
	for _, entry := range c.watchers[service] {
		// The onChange function is quick and non-blocking, so
		// it is permissible to call it while holding c.mu.
		entry.onChange()
	}
}

// Check implements Checker. It's safe to call concurrently with SetStatus.
func (c *StaticChecker) Check(_ context.Context, req *CheckRequest) (*CheckResponse, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if status, registered := c.statuses[req.Service]; registered {
		return &CheckResponse{Status: status}, nil
	}
	if req.Service == "" {
		return &CheckResponse{Status: StatusServing}, nil
	}
	return nil, connect.NewError(
		connect.CodeNotFound,
		fmt.Errorf("unknown service %s", req.Service),
	)
}

// Watch implements Watcher. It's safe to call concurrently with SetStatus.
func (c *StaticChecker) Watch(_ context.Context, req *CheckRequest, onChange func()) (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.watchers == nil {
		c.watchers = make(map[string][]*staticWatchEntry)
	}
	if _, registered := c.statuses[req.Service]; !registered && req.Service != "" {
		return nil, connect.NewError(
			connect.CodeNotFound,
			fmt.Errorf("unknown service %s", req.Service),
		)
	}
	entry := &staticWatchEntry{
		onChange: onChange,
	}
	c.watchers[req.Service] = append(c.watchers[req.Service], entry)
	return func() {
		c.removeWatcher(req.Service, entry)
	}, nil
}

func (c *StaticChecker) removeWatcher(service string, entry *staticWatchEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries := c.watchers[service]
	for i, e := range entries {
		if e != entry {
			continue
		}
		// Found it.
		c.watchers[service] = append(entries[:i], entries[i+1:]...)
		if len(c.watchers[service]) == 0 {
			delete(c.watchers, service)
		}
		return
	}
}
