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

	"connectrpc.com/connect"
	healthv1 "connectrpc.com/grpchealth/internal/gen/go/connectext/grpc/health/v1"
)

const (
	// HealthV1ServiceName is the fully-qualified name of the v1 version of the health service.
	HealthV1ServiceName = "grpc.health.v1.Health"

	checkProcedure = "/" + HealthV1ServiceName + "/Check"
	watchProcedure = "/" + HealthV1ServiceName + "/Watch"
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
// If the Checker also implements [Watcher], the returned handler supports the
// streaming Watch RPC. Otherwise, the Watch method returns
// connect.CodeUnimplemented.
//
// For more details on gRPC's health checking protocol, see
// https://github.com/grpc/grpc/blob/master/doc/health-checking.md and
// https://github.com/grpc/grpc/blob/master/src/proto/grpc/health/v1/health.proto.
func NewHandler(checker Checker, options ...connect.HandlerOption) (string, http.Handler) {
	const serviceName = "/" + HealthV1ServiceName + "/"
	hdlr := &handler{checker: checker}
	if watcher, ok := checker.(Watcher); ok {
		hdlr.watcher = watcher
	}
	mux := http.NewServeMux()
	mux.Handle(checkProcedure, connect.NewUnaryHandler(
		checkProcedure,
		hdlr.check,
		options...,
	))
	mux.Handle(watchProcedure, connect.NewServerStreamHandler(
		watchProcedure,
		hdlr.watch,
		options...,
	))
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

// A Watcher extends Checker with the ability to notify the caller when the
// health status of a service changes. When a Checker also implements Watcher,
// the handler returned by [NewHandler] supports the streaming Watch RPC.
type Watcher interface {
	Checker

	// Watch monitors the health of the requested service and calls onChange
	// whenever the status may have changed. Implementations should return
	// quickly rather than blocking. They may return an error if the request
	// cannot be satisfied (for example, if the service is unknown). The
	// context is only used for the duration of the Watch call itself; it
	// does not govern the lifetime of the watch.
	//
	// The returned stop function tells the implementation to stop calling
	// onChange. However, if two goroutines are racing, one calling stop and
	// the other calling onChange, this is fine. In other words, it is not
	// an error if onChange gets called after stop; but stop should arrange
	// for calls to onChange to cease.
	Watch(ctx context.Context, req *CheckRequest, onChange func()) (stop func(), err error)
}

type handler struct {
	checker Checker
	watcher Watcher // nil if checker does not implement Watcher
}

func (h *handler) check(
	ctx context.Context,
	req *connect.Request[healthv1.HealthCheckRequest],
) (*connect.Response[healthv1.HealthCheckResponse], error) {
	var checkRequest CheckRequest
	if req.Msg != nil {
		checkRequest.Service = req.Msg.GetService()
	}
	checkResponse, err := h.checker.Check(ctx, &checkRequest)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&healthv1.HealthCheckResponse{
		Status: healthv1.HealthCheckResponse_ServingStatus(checkResponse.Status),
	}), nil
}

func (h *handler) watch(
	ctx context.Context,
	req *connect.Request[healthv1.HealthCheckRequest],
	stream *connect.ServerStream[healthv1.HealthCheckResponse],
) error {
	if h.watcher == nil {
		return connect.NewError(
			connect.CodeUnimplemented,
			errors.New("watching health state is not supported"),
		)
	}
	var checkRequest CheckRequest
	checkRequest.Service = req.Msg.GetService()
	changed := make(chan struct{}, 1)
	stop, err := h.watcher.Watch(ctx, &checkRequest, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return err
	}
	defer stop()
	// Send the current status immediately.
	var lastStatus healthv1.HealthCheckResponse_ServingStatus
	if err := h.checkAndSend(ctx, &checkRequest, stream, &lastStatus, true); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
			if err := h.checkAndSend(ctx, &checkRequest, stream, &lastStatus, false); err != nil {
				return err
			}
		}
	}
}

func (h *handler) checkAndSend(
	ctx context.Context,
	req *CheckRequest,
	stream *connect.ServerStream[healthv1.HealthCheckResponse],
	lastStatus *healthv1.HealthCheckResponse_ServingStatus,
	forceSend bool,
) error {
	checkResponse, err := h.checker.Check(ctx, req)
	if err != nil {
		return err
	}
	status := healthv1.HealthCheckResponse_ServingStatus(checkResponse.Status)
	if status == *lastStatus && !forceSend {
		return nil
	}
	*lastStatus = status
	return stream.Send(&healthv1.HealthCheckResponse{Status: status})
}
