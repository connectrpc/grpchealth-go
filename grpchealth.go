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

// Package grpchealth enables any net/http server, including those built with
// Connect, to respond to gRPC-style health checks. This lets load balancers,
// container orchestrators, and other infrastructure systems respond to changes
// in your HTTP server's health.
//
// The exposed health-checking API is wire compatible with Google's gRPC
// implementations, so it works with grpcurl, grpc-health-probe, and Kubernetes
// gRPC liveness probes.
//
// The core Connect package is connectrpc.com/connect. Documentation is
// available at https://connectrpc.com.
package grpchealth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect"
	healthv1 "connectrpc.com/grpchealth/internal/gen/go/connectext/grpc/health/v1"
)

const (
	// HealthV1ServiceName is the fully-qualified name of the v1 version of the health service.
	HealthV1ServiceName = "grpc.health.v1.Health"

	serviceURIPath     = "/" + HealthV1ServiceName + "/"
	checkMethodURIPath = serviceURIPath + "Check"
	watchMethodURIPath = serviceURIPath + "Watch"
)

// Status describes the health of a service.
type Status uint8

const (
	// StatusUnknown indicates that the service's health state is indeterminate.
	StatusUnknown Status = 0

	// StatusServing indicates that the service is ready to accept requests.
	StatusServing Status = 1

	// StatusNotServing indicates that the process is healthy but the service is
	// not accepting requests. For example, StatusNotServing is often appropriate
	// when your primary database is down or unreachable.
	StatusNotServing Status = 2
)

// String representation of the status.
func (s Status) String() string {
	switch s {
	case StatusUnknown:
		return "unknown"
	case StatusServing:
		return "serving"
	case StatusNotServing:
		return "not_serving"
	}

	return fmt.Sprintf("status_%d", s)
}

// NewHandler wraps the supplied Checker to build an HTTP handler for gRPC's
// health-checking API. It returns the path on which to mount the handler and
// the HTTP handler itself.
//
// Note that the returned handler only supports the unary Check method, not the
// streaming Watch. As suggested in gRPC's health schema, it returns
// connect.CodeUnimplemented for the Watch method.
//
// For more details on gRPC's health checking protocol, see
// https://github.com/grpc/grpc/blob/master/doc/health-checking.md and
// https://github.com/grpc/grpc/blob/master/src/proto/grpc/health/v1/health.proto.
func NewHandler(checker Checker, options ...connect.HandlerOption) (string, http.Handler) {
	mux := http.NewServeMux()
	check := connect.NewUnaryHandler(
		checkMethodURIPath,
		func(
			ctx context.Context,
			req *connect.Request[healthv1.HealthCheckRequest],
		) (*connect.Response[healthv1.HealthCheckResponse], error) {
			var checkRequest CheckRequest
			if req.Msg != nil {
				checkRequest.Service = req.Msg.Service
			}
			checkResponse, err := checker.Check(ctx, &checkRequest)
			if err != nil {
				return nil, err
			}
			return connect.NewResponse(&healthv1.HealthCheckResponse{
				Status: healthv1.HealthCheckResponse_ServingStatus(checkResponse.Status),
			}), nil
		},
		options...,
	)
	mux.Handle(checkMethodURIPath, check)
	var watch *connect.Handler
	if watcher, ok := checker.(Watcher); ok {
		watch = connect.NewServerStreamHandler(
			watchMethodURIPath,
			func(
				ctx context.Context,
				req *connect.Request[healthv1.HealthCheckRequest],
				stream *connect.ServerStream[healthv1.HealthCheckResponse],
			) error {
				var checkRequest CheckRequest
				if req.Msg != nil {
					checkRequest.Service = req.Msg.Service
				}
				done := make(chan struct{})
				var rpcErr error
				stop := watcher.Watch(ctx, &checkRequest, func(resp *CheckResponse, err error) {
					if err == nil {
						err = stream.Send(&healthv1.HealthCheckResponse{
							Status: healthv1.HealthCheckResponse_ServingStatus(resp.Status),
						})
					}
					if err != nil {
						rpcErr = err
						close(done)
						return
					}
				})
				defer stop()
				<-done
				return rpcErr
			},
			options...,
		)
	} else {
		watch = connect.NewServerStreamHandler(
			watchMethodURIPath,
			func(
				_ context.Context,
				_ *connect.Request[healthv1.HealthCheckRequest],
				_ *connect.ServerStream[healthv1.HealthCheckResponse],
			) error {
				return connect.NewError(
					connect.CodeUnimplemented,
					errors.New("this server doesn't support watching health state"),
				)
			},
			options...,
		)
	}
	mux.Handle(watchMethodURIPath, watch)
	return serviceURIPath, mux
}

// CheckRequest is a request for the health of a service. When using protobuf,
// Service will be a fully-qualified service name (for example,
// "acme.ping.v1.PingService"). If the Service is an empty string, the caller
// is asking for the health status of whole process.
type CheckRequest struct {
	Service string
}

// CheckResponse reports the health of a service (or of the whole process). The
// only valid Status values are StatusUnknown, StatusServing, and
// StatusNotServing. When asked to report on the status of an unknown service,
// Checkers should return a connect.CodeNotFound error.
//
// Often, systems monitoring health respond to errors by restarting the
// process. They often respond to StatusNotServing by removing the process from
// a load balancer pool.
type CheckResponse struct {
	Status Status
}

// A Checker reports the health of a service. It must be safe to call
// concurrently.
type Checker interface {
	Check(context.Context, *CheckRequest) (*CheckResponse, error)
}

// Watcher is an extension of Checker: in addition to polling for the
// health state, it also supports  notification via callbacks. It must
// be safe to call concurrently.
type Watcher interface {
	Checker
	// Watch will call the given onUpdate function with the status and
	// then call it repeatedly thereafter as the status changes, until
	// the update has a non-nil error or until the returned stop function
	// is called. When the update has a non-nil error, that will be the
	// final invocation. If the stop function is called, there will be
	// no further calls.
	//
	// The given function does not need to be thread-safe. Calls to it
	// must be serialized to guarantee in-order delivery of state changes.
	// State transitions may be elided/debounced, such as if the function
	// processes too slowly and/or if there is a sudden burst of rapid
	// state changes.
	Watch(ctx context.Context, request *CheckRequest, onUpdate func(*CheckResponse, error)) (stop func())
}

// StaticChecker is a simple Checker implementation. It always returns
// StatusServing for the process, and it returns a static value for each
// service.
//
// If you have a dynamic list of services, want to ping a database as part of
// your health check, or otherwise need something more specialized, you should
// write a custom Checker implementation.
type StaticChecker struct {
	mu         sync.RWMutex
	statuses   map[string]Status
	watchers   map[string]map[int64]*watchNotifier
	watchCount int64
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
	return &StaticChecker{
		statuses: statuses,
		watchers: make(map[string]map[int64]*watchNotifier),
	}
}

// SetStatus sets the health status of a service, registering a new service if
// necessary. It's safe to call SetStatus, Check, and Watch concurrently.
//
// If the given service name is empty, it sets a server-wide status that is
// returned to check requests that do not request a particular service. If no
// such status is ever set, checks that do not request a particular service
// will get a response of StatusServing.
func (c *StaticChecker) SetStatus(service string, status Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses[service] = status
	for _, watcher := range c.watchers[service] {
		watcher.notify(status, nil)
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

// Watch implements optional watch functionality. It's safe to call concurrently
// with SetStatus.
func (c *StaticChecker) Watch(ctx context.Context, req *CheckRequest, onUpdate func(*CheckResponse, error)) (stop func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	service := req.Service
	status, registered := c.statuses[service]
	if !registered {
		if service != "" {
			go onUpdate(nil, connect.NewError(
				connect.CodeNotFound,
				fmt.Errorf("unknown service %s", service),
			))
			return func() {}
		}
		status = StatusServing
	}
	notifier := newNotifier(onUpdate, status)
	watcherID := c.watchCount
	c.watchCount++
	watchers := c.watchers[service]
	if watchers == nil {
		watchers = make(map[int64]*watchNotifier)
		c.watchers[service] = watchers
	}
	watchers[watcherID] = notifier
	context.AfterFunc(ctx, func() {
		notifier.notify(0, ctx.Err())
		c.deleteWatcher(service, watcherID)
	})
	return func() {
		notifier.stop()
		c.deleteWatcher(service, watcherID)
	}
}

func (c *StaticChecker) deleteWatcher(service string, watcherID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.watchers[service], watcherID)
}

// watchNotifier handles serializing calls to the given notifyFn.
// If the notifyFn executes too slowly, status changes will be de-bounced.
// So it will always be called with the latest status, but it may not be
// called with interstitial updates that occurred while it was running,
// process the previous status change.
type watchNotifier struct {
	notifyFn func(*CheckResponse, error)
	stopped  atomic.Bool

	mu         sync.Mutex
	delivering bool
	status     Status
	err        error
}

func newNotifier(notifyFn func(*CheckResponse, error), status Status) *watchNotifier {
	notifier := &watchNotifier{
		notifyFn:   notifyFn,
		delivering: true,
		status:     status,
	}
	notifier.deliverNotices() // deliver the initial status
	return notifier
}

func (w *watchNotifier) stop() {
	w.stopped.Store(true)
}

func (w *watchNotifier) notify(status Status, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.err != nil {
		return // already delivered final notice
	}
	if err == nil && w.status == status {
		return // no change to deliver
	}
	if w.stopped.Load() {
		return // stopped, so don't deliver anymore
	}

	w.status, w.err = status, err
	if !w.delivering {
		w.delivering = true
		go w.deliverNotices()
	}
}

func (w *watchNotifier) deliverNotices() {
	var prevStatus Status
	first := true
	for {
		w.mu.Lock()
		status, err := w.status, w.err
		if !first && status == prevStatus && err == nil {
			// no change to deliver
			w.delivering = false
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()

		if err != nil {
			if w.stopped.CompareAndSwap(false, true) {
				w.notifyFn(nil, err)
			}
			return
		}

		if w.stopped.Load() {
			return
		}
		w.notifyFn(&CheckResponse{Status: status}, nil)
		prevStatus = status
		first = false
	}
}
