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

	"connectrpc.com/connect"
	healthv1 "connectrpc.com/grpchealth/internal/gen/go/connectext/grpc/health/v1"
)

// HealthV1ServiceName is the fully-qualified name of the v1 version of the health service.
const HealthV1ServiceName = "grpc.health.v1.Health"

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
// If the Checker also implements [Watcher], the streaming Watch RPC is
// enabled. Otherwise, Watch returns connect.CodeUnimplemented as suggested
// in gRPC's health schema.
//
// For more details on gRPC's health checking protocol, see
// https://github.com/grpc/grpc/blob/master/doc/health-checking.md and
// https://github.com/grpc/grpc/blob/master/src/proto/grpc/health/v1/health.proto.
func NewHandler(checker Checker, options ...connect.HandlerOption) (string, http.Handler) {
	const serviceName = "/grpc.health.v1.Health/"
	mux := http.NewServeMux()
	check := connect.NewUnaryHandler(
		serviceName+"Check",
		func(
			ctx context.Context,
			req *connect.Request[healthv1.HealthCheckRequest],
		) (*connect.Response[healthv1.HealthCheckResponse], error) {
			var checkRequest CheckRequest
			if req.Msg != nil {
				checkRequest.Service = req.Msg.GetService()
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
	mux.Handle(serviceName+"Check", check)
	watcher, isWatcher := checker.(Watcher)
	watchHandler := connect.NewServerStreamHandler(
		serviceName+"Watch",
		func(
			ctx context.Context,
			req *connect.Request[healthv1.HealthCheckRequest],
			stream *connect.ServerStream[healthv1.HealthCheckResponse],
		) error {
			if !isWatcher {
				return connect.NewError(
					connect.CodeUnimplemented,
					errors.New("watching health state is not supported"),
				)
			}
			var checkRequest CheckRequest
			if req.Msg != nil {
				checkRequest.Service = req.Msg.GetService()
			}
			return watcher.Watch(ctx, &checkRequest, func(resp *CheckResponse) error {
				return stream.Send(&healthv1.HealthCheckResponse{
					Status: healthv1.HealthCheckResponse_ServingStatus(resp.Status),
				})
			})
		},
		options...,
	)
	mux.Handle(serviceName+"Watch", watchHandler)
	return serviceName, mux
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

// A Watcher streams health status changes for a service. If a [Checker] also
// implements Watcher, [NewHandler] enables the streaming Watch RPC.
//
// Watch must send the current status for the requested service immediately,
// then block and call update for each subsequent status change. For unknown
// services, Watch should return a connect.CodeNotFound error. Watch must
// return when ctx is done and must not call update after returning.
//
// Implementations must be safe to call concurrently.
type Watcher interface {
	Watch(ctx context.Context, req *CheckRequest, update func(*CheckResponse) error) error
}

// StaticChecker is a simple Checker and [Watcher] implementation. It always
// returns StatusServing for the process, and it returns a static value for
// each service. Status changes are broadcast to any active Watch streams.
//
// If you have a dynamic list of services, want to ping a database as part of
// your health check, or otherwise need something more specialized, you should
// write a custom Checker and Watcher implementation.
type StaticChecker struct {
	mu       sync.RWMutex
	statuses map[string]Status
	// watchers maps service names to the set of active watch channels.
	// Each channel has capacity 1 to allow non-blocking sends.
	watchers map[string]map[chan Status]struct{}
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
		watchers: make(map[string]map[chan Status]struct{}),
	}
}

// SetStatus sets the health status of a service, registering a new service if
// necessary. It's safe to call SetStatus, Check, and Watch concurrently.
//
// If the given service name is empty, it sets a server-wide status that is
// returned to check requests that do not request a particular service. If no
// such status is ever set, checks that do not request a particular service
// will get a response of StatusServing.
//
// Any active Watch streams for the service are notified of the change.
func (c *StaticChecker) SetStatus(service string, status Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses[service] = status
	for statusChan := range c.watchers[service] {
		// Drain any pending value before sending to ensure the watcher
		// always receives the latest status.
		select {
		case statusChan <- status:
		case <-statusChan:
			statusChan <- status
		}
	}
}

// Check implements [Checker]. It's safe to call concurrently with SetStatus.
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

// Watch implements [Watcher]. It sends the current status for the requested
// service immediately, then blocks and calls update for each subsequent
// status change until ctx is done.
func (c *StaticChecker) Watch(ctx context.Context, req *CheckRequest, update func(*CheckResponse) error) error {
	service := req.Service
	statusChan := make(chan Status, 1)

	c.mu.Lock()
	// Send initial status while holding the lock, so no update is missed
	// between reading the current status and registering the channel.
	status, registered := c.statuses[service]
	if !registered {
		if service == "" {
			status = StatusServing
		} else {
			c.mu.Unlock()
			return connect.NewError(
				connect.CodeNotFound,
				fmt.Errorf("unknown service %s", service),
			)
		}
	}

	if c.watchers[service] == nil {
		c.watchers[service] = make(map[chan Status]struct{})
	}

	c.watchers[service][statusChan] = struct{}{}
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.watchers[service], statusChan)
		if len(c.watchers[service]) == 0 {
			delete(c.watchers, service)
		}
		c.mu.Unlock()
	}()

	// Send the initial status outside the lock.
	if err := update(&CheckResponse{Status: status}); err != nil {
		return err
	}
	lastStatus := status

	for {
		select {
		case <-ctx.Done():
			return nil
		case newStatus := <-statusChan:
			if newStatus != lastStatus {
				if err := update(&CheckResponse{Status: newStatus}); err != nil {
					return err
				}
				lastStatus = newStatus
			}
		}
	}
}
