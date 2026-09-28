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
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
)

func TestClient_Check(t *testing.T) {
	const (
		userFQN = "acme.user.v1.UserService"
		unknown = "foobar"
	)
	t.Parallel()
	checker := NewStaticChecker(userFQN)
	mux := http.NewServeMux()
	connectServer := connect.NewServer()
	Register(connectServer, checker)
	connecthttp.Mount(mux, connectServer)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := NewClient(connect.NewClient(connecthttp.NewTransport(
		server.Client(),
		server.URL,
		connecthttp.WithGRPC(),
	)))

	assertStatus := func(t *testing.T, service string, expect Status) {
		t.Helper()
		resp, err := client.Check(t.Context(), &CheckRequest{Service: service})
		if err != nil {
			t.Fatal(err.Error())
		}
		if resp.Status != expect {
			t.Fatalf("got status %v, expected %v", resp.Status, expect)
		}
	}

	// Process-wide health.
	assertStatus(t, "", StatusServing)
	checker.SetStatus("", StatusNotServing)
	assertStatus(t, "", StatusNotServing)
	checker.SetStatus("", StatusServing)
	assertStatus(t, "", StatusServing)

	// Named service.
	assertStatus(t, userFQN, StatusServing)
	checker.SetStatus(userFQN, StatusNotServing)
	assertStatus(t, userFQN, StatusNotServing)

	// Unknown service returns CodeNotFound.
	_, err := client.Check(t.Context(), &CheckRequest{Service: unknown})
	if err == nil {
		t.Fatalf("expected error checking unknown service %q", unknown)
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("got %v (%T), expected a *connect.Error", err, err)
	}
	if code := connectErr.Code(); code != connect.CodeNotFound {
		t.Fatalf("got code %v, expected CodeNotFound", code)
	}
}

func TestClient_Watch(t *testing.T) {
	const userFQN = "acme.user.v1.UserService"
	t.Parallel()
	checker := NewStaticChecker(userFQN)
	mux := http.NewServeMux()
	connectServer := connect.NewServer()
	Register(connectServer, checker)
	connecthttp.Mount(mux, connectServer)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := NewClient(connect.NewClient(connecthttp.NewTransport(
		server.Client(),
		server.URL,
		connecthttp.WithGRPC(),
	)))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events := client.Watch(ctx, &CheckRequest{Service: userFQN})

	recv := func(t *testing.T, expect Status) {
		t.Helper()
		event, ok := <-events
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		if event.Err != nil {
			t.Fatalf("unexpected error: %v", event.Err)
		}
		if event.Response.Status != expect {
			t.Fatalf("got status %v, expected %v", event.Response.Status, expect)
		}
	}

	// First status is delivered immediately.
	recv(t, StatusServing)

	// Status transitions propagate through the channel.
	checker.SetStatus(userFQN, StatusNotServing)
	recv(t, StatusNotServing)

	checker.SetStatus(userFQN, StatusServing)
	recv(t, StatusServing)

	// Canceling the context closes the channel.
	cancel()
	for range events { //nolint:revive // Drain until the channel closes.
	}
}

func TestClient_Watch_unknownService(t *testing.T) {
	t.Parallel()
	checker := NewStaticChecker()
	mux := http.NewServeMux()
	connectServer := connect.NewServer()
	Register(connectServer, checker)
	connecthttp.Mount(mux, connectServer)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := NewClient(connect.NewClient(connecthttp.NewTransport(
		server.Client(),
		server.URL,
		connecthttp.WithGRPC(),
	)))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	gotErr := false
	for event := range client.Watch(ctx, &CheckRequest{Service: "unknown.Service"}) {
		if event.Err == nil {
			t.Fatalf("expected error, got response: %v", event.Response)
		}
		var connectErr *connect.Error
		if !errors.As(event.Err, &connectErr) {
			t.Fatalf("got %v (%T), expected a *connect.Error", event.Err, event.Err)
		}
		if code := connectErr.Code(); code != connect.CodeNotFound {
			t.Fatalf("got code %v, expected CodeNotFound", code)
		}
		gotErr = true
	}
	if !gotErr {
		t.Fatal("channel closed without delivering an error event")
	}
}
