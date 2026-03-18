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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"connectrpc.com/connect"
	healthv1 "connectrpc.com/grpchealth/internal/gen/go/connectext/grpc/health/v1"
)

func TestCode(t *testing.T) {
	t.Parallel()

	knownStatuses := map[Status]struct{}{
		StatusUnknown:    {},
		StatusServing:    {},
		StatusNotServing: {},
	}
	check := func(s Status) bool {
		got := s.String()
		_, known := knownStatuses[s]
		return known != strings.HasPrefix(got, "status_")
	}
	// always check named statuses
	for status := range knownStatuses {
		if !check(status) {
			t.Fatalf("expected string representation of %q to be customized", status)
		}
	}
	// probabilistically explore other statuses
	if err := quick.Check(check, nil /* config */); err != nil {
		t.Fatal(err)
	}
}

func TestHealth_Check(t *testing.T) {
	const (
		userFQN = "acme.user.v1.UserService"
		unknown = "foobar"
	)
	t.Parallel()
	mux := http.NewServeMux()
	checker := NewStaticChecker(userFQN)
	mux.Handle(NewHandler(checker))
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := connect.NewClient[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse](
		server.Client(),
		server.URL+checkProcedure,
		connect.WithGRPC(),
	)

	assertStatus := func(
		t *testing.T,
		service string,
		expect Status,
	) {
		t.Helper()
		res, err := client.CallUnary(
			t.Context(),
			connect.NewRequest(&healthv1.HealthCheckRequest{Service: service}),
		)
		if err != nil {
			t.Fatal(err.Error())
		}
		if Status(res.Msg.GetStatus()) != expect { //nolint:gosec // Conversion is safe here
			t.Fatalf("got status %v, expected %v", res.Msg.GetStatus(), expect)
		}
	}
	assertUnknown := func(
		t *testing.T,
		service string,
	) {
		t.Helper()
		_, err := client.CallUnary(
			t.Context(),
			connect.NewRequest(&healthv1.HealthCheckRequest{Service: service}),
		)
		if err == nil {
			t.Fatalf("expected error checking unknown service %q", service)
		}
		var connectErr *connect.Error
		if ok := errors.As(err, &connectErr); !ok {
			t.Fatalf("got %v (%T), expected a *connect.Error", err, err)
		}
		if code := connectErr.Code(); code != connect.CodeNotFound {
			t.Fatalf("check %q: got code %v, expected CodeNotFound", service, code)
		}
	}

	assertStatus(t, "" /* process */, StatusServing)
	checker.SetStatus("", StatusNotServing)
	assertStatus(t, "", StatusNotServing)
	checker.SetStatus("", StatusServing)
	assertStatus(t, "", StatusServing)

	assertStatus(t, userFQN, StatusServing)
	checker.SetStatus(userFQN, StatusNotServing)
	assertStatus(t, userFQN, StatusNotServing)

	assertUnknown(t, unknown)
	checker.SetStatus(unknown, StatusServing)
	assertStatus(t, unknown, StatusServing)
}

func TestHealth_Watch(t *testing.T) {
	const (
		userFQN = "acme.user.v1.UserService"
		pingFQN = "acme.ping.v1.PingService"
	)
	t.Parallel()
	checker := NewStaticChecker(userFQN, pingFQN)
	mux := http.NewServeMux()
	mux.Handle(NewHandler(checker))
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	watchClient := connect.NewClient[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse](
		server.Client(),
		server.URL+watchProcedure,
		connect.WithGRPC(),
	)
	openStream := func(ctx context.Context, service string) *connect.ServerStreamForClient[healthv1.HealthCheckResponse] {
		t.Helper()
		stream, err := watchClient.CallServerStream(
			ctx,
			connect.NewRequest(&healthv1.HealthCheckRequest{Service: service}),
		)
		if err != nil {
			t.Fatal(err.Error())
		}
		return stream
	}
	receiveStatus := func(t *testing.T, stream *connect.ServerStreamForClient[healthv1.HealthCheckResponse], expect Status) {
		t.Helper()
		if ok := stream.Receive(); !ok {
			t.Fatalf("expected message from Watch, got error: %v", stream.Err())
		}
		if got := Status(stream.Msg().GetStatus()); got != expect { //nolint:gosec
			t.Fatalf("got status %v, expected %v", got, expect)
		}
	}

	// Watching an unknown service should return CodeNotFound.
	unknownStream := openStream(t.Context(), "unknown.Service")
	defer unknownStream.Close()
	if ok := unknownStream.Receive(); ok {
		t.Fatal("expected error from Watch on unknown service, got message")
	}
	var connectErr *connect.Error
	if !errors.As(unknownStream.Err(), &connectErr) {
		t.Fatalf("got %v (%T), expected a *connect.Error", unknownStream.Err(), unknownStream.Err())
	}
	if code := connectErr.Code(); code != connect.CodeNotFound {
		t.Fatalf("got code %v, expected CodeNotFound", code)
	}

	// Open three streams: two named services and the process-wide
	// empty string. The empty string has no registered status, but
	// should still succeed since it represents overall process health.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	userStream := openStream(ctx, userFQN)
	defer userStream.Close()
	pingStream := openStream(ctx, pingFQN)
	defer pingStream.Close()
	processStream := openStream(ctx, "")
	defer processStream.Close()

	// All three should receive their current status immediately.
	receiveStatus(t, userStream, StatusServing)
	receiveStatus(t, pingStream, StatusServing)
	receiveStatus(t, processStream, StatusServing)

	// Changing one service should only notify that service's stream.
	// If a spurious message were sent to another stream, it would
	// cause a later receive on that stream to return the wrong status.
	checker.SetStatus(userFQN, StatusNotServing)
	receiveStatus(t, userStream, StatusNotServing)

	checker.SetStatus(pingFQN, StatusNotServing)
	receiveStatus(t, pingStream, StatusNotServing)

	// The process-wide stream should respond to SetStatus("").
	checker.SetStatus("", StatusNotServing)
	receiveStatus(t, processStream, StatusNotServing)

	// De-duplication: setting the same status should not produce a
	// message. Set the same, then a different status, and verify we
	// only receive the different one.
	checker.SetStatus(userFQN, StatusNotServing)
	checker.SetStatus(userFQN, StatusServing)
	receiveStatus(t, userStream, StatusServing)

	// Cancel the context and verify the streams end.
	cancel()
	for userStream.Receive() {
	}
	for pingStream.Receive() {
	}
	for processStream.Receive() {
	}
}

func TestWatchUnimplemented(t *testing.T) {
	t.Parallel()
	checker := checkerFunc(func(_ context.Context, _ *CheckRequest) (*CheckResponse, error) {
		return &CheckResponse{Status: StatusServing}, nil
	})
	mux := http.NewServeMux()
	mux.Handle(NewHandler(checker))
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	watchClient := connect.NewClient[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse](
		server.Client(),
		server.URL+watchProcedure,
		connect.WithGRPC(),
	)
	stream, err := watchClient.CallServerStream(
		t.Context(),
		connect.NewRequest(&healthv1.HealthCheckRequest{Service: "anything"}),
	)
	if err != nil {
		t.Fatal(err.Error())
	}
	defer stream.Close()
	if ok := stream.Receive(); ok {
		t.Fatalf("got message from Watch")
	}
	if stream.Err() == nil {
		t.Fatalf("expected error from stream")
	}
	var connectErr *connect.Error
	if ok := errors.As(stream.Err(), &connectErr); !ok {
		t.Fatalf("got %v (%T), expected a *connect.Error", stream.Err(), stream.Err())
	}
	if code := connectErr.Code(); code != connect.CodeUnimplemented {
		t.Fatalf("got code %v, expected CodeUnimplemented", code)
	}
}

// checkerFunc is a Checker that does not implement Watcher.
type checkerFunc func(context.Context, *CheckRequest) (*CheckResponse, error)

func (f checkerFunc) Check(ctx context.Context, req *CheckRequest) (*CheckResponse, error) {
	return f(ctx, req)
}
