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
			context.Background(),
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
			context.Background(),
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

	watcher := connect.NewClient[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse](
		server.Client(),
		server.URL+"/grpc.health.v1.Health/Watch",
		connect.WithGRPC(),
	)
	stream, err := watcher.CallServerStream(
		context.Background(),
		connect.NewRequest(&healthv1.HealthCheckRequest{Service: userFQN}),
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
		t.Fatalf("got %v (%T), expected a *connect.Error", err, err)
	}
	if code := connectErr.Code(); code != connect.CodeUnimplemented {
		t.Fatalf("got code %v, expected CodeUnimplemented", code)
	}
}
