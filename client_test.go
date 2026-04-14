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
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
)

func TestClient_Check(t *testing.T) {
	const (
		userFQN = "acme.user.v1.UserService"
		unknown = "foobar"
	)
	t.Parallel()
	checker := NewStaticChecker(userFQN)
	mux := http.NewServeMux()
	mux.Handle(NewHandler(checker))
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := NewClient(server.Client(), server.URL, connect.WithGRPC())

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
	mux.Handle(NewHandler(checker))
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := NewClient(server.Client(), server.URL, connect.WithGRPC())

	// Use iter.Pull2 to step through the Watch iterator, so we can
	// interleave SetStatus calls between receives.
	watchIter, stop := client.Watch(t.Context(), &CheckRequest{Service: userFQN})
	defer stop()
	next, pullStop := iter.Pull2(watchIter)
	defer pullStop()

	pull := func(t *testing.T, expect Status) {
		t.Helper()
		resp, err, ok := next()
		if !ok {
			t.Fatal("iterator stopped unexpectedly")
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Status != expect {
			t.Fatalf("got status %v, expected %v", resp.Status, expect)
		}
	}

	// First status is delivered immediately.
	pull(t, StatusServing)

	// Status transitions propagate through the iterator.
	checker.SetStatus(userFQN, StatusNotServing)
	pull(t, StatusNotServing)

	checker.SetStatus(userFQN, StatusServing)
	pull(t, StatusServing)

	// Stopping cancels the stream; the iterator drains.
	stop()
	for {
		_, _, ok := next()
		if !ok {
			break
		}
	}
}

func TestClient_Watch_unknownService(t *testing.T) {
	t.Parallel()
	checker := NewStaticChecker()
	mux := http.NewServeMux()
	mux.Handle(NewHandler(checker))
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := NewClient(server.Client(), server.URL, connect.WithGRPC())

	watchIter, stop := client.Watch(t.Context(), &CheckRequest{Service: "unknown.Service"})
	defer stop()
	for resp, err := range watchIter {
		if err == nil {
			t.Fatalf("expected error, got response: %v", resp)
		}
		var connectErr *connect.Error
		if !errors.As(err, &connectErr) {
			t.Fatalf("got %v (%T), expected a *connect.Error", err, err)
		}
		if code := connectErr.Code(); code != connect.CodeNotFound {
			t.Fatalf("got code %v, expected CodeNotFound", code)
		}
	}
}
