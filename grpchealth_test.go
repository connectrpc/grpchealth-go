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

func TestHealth(t *testing.T) {
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
		server.URL+"/grpc.health.v1.Health/Check",
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

func TestWatch(t *testing.T) {
	t.Parallel()

	checker := NewStaticChecker(
		"acme.watch.v1.StreamService",
		"acme.watch.v1.UnknownTest",
		"acme.watch.v1.EmptyTest",
		"acme.watch.v1.DuplicateTest",
	)
	mux := http.NewServeMux()
	mux.Handle(NewHandler(checker))
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	newWatchClient := func() *connect.Client[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse] {
		return connect.NewClient[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse](
			server.Client(),
			server.URL+"/grpc.health.v1.Health/Watch",
			connect.WithGRPC(),
		)
	}

	t.Run("streams status updates", func(t *testing.T) {
		t.Parallel()
		const service = "acme.watch.v1.StreamService"

		watcher := newWatchClient()
		ctx, cancel := context.WithCancel(t.Context())

		stream, err := watcher.CallServerStream(
			ctx,
			connect.NewRequest(&healthv1.HealthCheckRequest{Service: service}),
		)
		if err != nil {
			cancel()
			t.Fatal(err.Error())
		}

		defer stream.Close()
		defer cancel()

		// Should receive initial status.
		if ok := stream.Receive(); !ok {
			t.Fatalf("expected initial status, got error: %v", stream.Err())
		}

		if got := Status(stream.Msg().GetStatus()); got != StatusServing { //nolint:gosec // Conversion is safe here
			t.Fatalf("got initial status %v, expected %v", got, StatusServing)
		}

		// Change status and expect an update.
		checker.SetStatus(service, StatusNotServing)

		if ok := stream.Receive(); !ok {
			t.Fatalf("expected status update, got error: %v", stream.Err())
		}

		if got := Status(stream.Msg().GetStatus()); got != StatusNotServing { //nolint:gosec // Conversion is safe here
			t.Fatalf("got status %v, expected %v", got, StatusNotServing)
		}

		// Change back and expect another update.
		checker.SetStatus(service, StatusServing)

		if ok := stream.Receive(); !ok {
			t.Fatalf("expected status update, got error: %v", stream.Err())
		}

		if got := Status(stream.Msg().GetStatus()); got != StatusServing { //nolint:gosec // Conversion is safe here
			t.Fatalf("got status %v, expected %v", got, StatusServing)
		}
	})

	t.Run("unknown service returns CodeNotFound", func(t *testing.T) {
		t.Parallel()
		const service = "acme.watch.v1.NeverRegistered"

		watcher := newWatchClient()
		stream, err := watcher.CallServerStream(
			t.Context(),
			connect.NewRequest(&healthv1.HealthCheckRequest{Service: service}),
		)
		if err != nil {
			t.Fatal(err.Error())
		}
		defer stream.Close()

		if ok := stream.Receive(); ok {
			t.Fatal("expected error for unknown service, got message")
		}
		var connectErr *connect.Error
		if ok := errors.As(stream.Err(), &connectErr); !ok {
			t.Fatalf("got %v (%T), expected a *connect.Error", stream.Err(), stream.Err())
		}
		if code := connectErr.Code(); code != connect.CodeNotFound {
			t.Fatalf("got code %v, expected CodeNotFound", code)
		}
	})

	t.Run("empty service watches process health", func(t *testing.T) {
		t.Parallel()

		watcher := newWatchClient()
		ctx, cancel := context.WithCancel(t.Context())

		stream, err := watcher.CallServerStream(
			ctx,
			connect.NewRequest(&healthv1.HealthCheckRequest{Service: ""}),
		)
		if err != nil {
			cancel()
			t.Fatal(err.Error())
		}

		defer stream.Close()
		defer cancel()

		// Empty service defaults to StatusServing.
		if ok := stream.Receive(); !ok {
			t.Fatalf("expected initial status, got error: %v", stream.Err())
		}

		if got := Status(stream.Msg().GetStatus()); got != StatusServing { //nolint:gosec // Conversion is safe here
			t.Fatalf("got initial status %v, expected %v", got, StatusServing)
		}
	})

	t.Run("duplicate status is not sent", func(t *testing.T) {
		t.Parallel()

		const service = "acme.watch.v1.DuplicateTest"

		watcher := newWatchClient()
		ctx, cancel := context.WithCancel(t.Context())

		stream, err := watcher.CallServerStream(
			ctx,
			connect.NewRequest(&healthv1.HealthCheckRequest{Service: service}),
		)
		if err != nil {
			cancel()
			t.Fatal(err.Error())
		}

		defer stream.Close()
		defer cancel()

		// Receive initial status.
		if ok := stream.Receive(); !ok {
			t.Fatalf("expected initial status, got error: %v", stream.Err())
		}
		// Set the same status -- should not produce a new message.
		checker.SetStatus(service, StatusServing)
		// Set a different status -- this should be the next message.
		checker.SetStatus(service, StatusNotServing)

		if ok := stream.Receive(); !ok {
			t.Fatalf("expected status update, got error: %v", stream.Err())
		}

		if got := Status(stream.Msg().GetStatus()); got != StatusNotServing { //nolint:gosec // Conversion is safe here
			t.Fatalf("got status %v, expected %v", got, StatusNotServing)
		}
	})
}

func TestWatchUnimplementedWithoutWatcher(t *testing.T) {
	t.Parallel()

	// Use a checker that does not implement Watcher.
	checker := checkerFunc(func(_ context.Context, _ *CheckRequest) (*CheckResponse, error) {
		return &CheckResponse{Status: StatusServing}, nil
	})
	mux := http.NewServeMux()
	mux.Handle(NewHandler(checker))
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	watcher := connect.NewClient[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse](
		server.Client(),
		server.URL+"/grpc.health.v1.Health/Watch",
		connect.WithGRPC(),
	)

	stream, err := watcher.CallServerStream(
		t.Context(),
		connect.NewRequest(&healthv1.HealthCheckRequest{Service: "anything"}),
	)
	if err != nil {
		t.Fatal(err.Error())
	}

	defer stream.Close()

	if ok := stream.Receive(); ok {
		t.Fatalf("got message from Watch, expected error")
	}

	if stream.Err() == nil {
		t.Fatal("expected error from stream")
	}

	var connectErr *connect.Error
	if ok := errors.As(stream.Err(), &connectErr); !ok {
		t.Fatalf("got %v (%T), expected a *connect.Error", stream.Err(), stream.Err())
	}

	if code := connectErr.Code(); code != connect.CodeUnimplemented {
		t.Fatalf("got code %v, expected CodeUnimplemented", code)
	}
}

// checkerFunc adapts a function to the Checker interface without
// implementing Watcher, useful for testing the unimplemented path.
type checkerFunc func(context.Context, *CheckRequest) (*CheckResponse, error)

func (f checkerFunc) Check(ctx context.Context, req *CheckRequest) (*CheckResponse, error) {
	return f(ctx, req)
}
