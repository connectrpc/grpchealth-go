// Copyright 2022 Buf Technologies, Inc.
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
	"testing"

	"github.com/bufbuild/connect-go"
	healthv1 "github.com/bufbuild/connect-grpchealth-go/internal/gen/go/connectext/grpc/health/v1"
)

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

	t.Run("process", func(t *testing.T) {
		t.Parallel()
		res, err := client.CallUnary(
			context.Background(),
			connect.NewRequest(&healthv1.HealthCheckRequest{}),
		)
		if err != nil {
			t.Fatalf(err.Error())
		}
		if Status(res.Msg.Status) != StatusServing {
			t.Fatalf("got status %v, expected %v", res.Msg.Status, StatusServing)
		}
	})
	t.Run("known", func(t *testing.T) {
		t.Parallel()
		res, err := client.CallUnary(
			context.Background(),
			connect.NewRequest(&healthv1.HealthCheckRequest{Service: userFQN}),
		)
		if err != nil {
			t.Fatalf(err.Error())
		}
		if Status(res.Msg.Status) != StatusServing {
			t.Fatalf("got status %v, expected %v", res.Msg.Status, StatusServing)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		t.Parallel()
		_, err := client.CallUnary(
			context.Background(),
			connect.NewRequest(&healthv1.HealthCheckRequest{Service: unknown}),
		)
		if err == nil {
			t.Fatalf("expected error checking unknown service")
		}
		var connectErr *connect.Error
		if ok := errors.As(err, &connectErr); !ok {
			t.Fatalf("got %v (%T), expected a *connect.Error", err, err)
		}
		if code := connectErr.Code(); code != connect.CodeNotFound {
			t.Fatalf("got code %v, expected CodeNotFound", code)
		}
	})
	t.Run("watch", func(t *testing.T) {
		t.Parallel()
		client := connect.NewClient[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse](
			server.Client(),
			server.URL+"/grpc.health.v1.Health/Watch",
			connect.WithGRPC(),
		)
		stream, err := client.CallServerStream(
			context.Background(),
			connect.NewRequest(&healthv1.HealthCheckRequest{Service: userFQN}),
		)
		if err != nil {
			t.Fatalf(err.Error())
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
	})
}
