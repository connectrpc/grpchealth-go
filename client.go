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
	"strings"

	"connectrpc.com/connect"
	healthv1 "connectrpc.com/grpchealth/internal/gen/go/connectext/grpc/health/v1"
)

// Compile-time assertion: *Client implements Checker.
var _ Checker = (*Client)(nil)

// Client calls the gRPC health-checking API. It implements [Checker], so it
// can be used anywhere a Checker is expected. It is safe to use concurrently.
type Client struct {
	check *connect.Client[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse]
	watch *connect.Client[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse]
}

// NewClient constructs a new Client for the gRPC health-checking API.
//
// The URL supplied should be the base URL of the Connect or gRPC server (for
// example, https://api.example.com). By default the client uses the Connect
// protocol with the binary Protobuf codec. To use the gRPC protocol, supply
// [connect.WithGRPC] as an option.
func NewClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		check: connect.NewClient[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse](
			httpClient,
			baseURL+checkProcedure,
			opts...,
		),
		watch: connect.NewClient[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse](
			httpClient,
			baseURL+watchProcedure,
			opts...,
		),
	}
}

// Check reports the health of the named service. An empty service name
// requests the health of the whole server process. If the service is unknown,
// the returned error will have [connect.CodeNotFound].
func (c *Client) Check(ctx context.Context, req *CheckRequest) (*CheckResponse, error) {
	res, err := c.check.CallUnary(ctx, connect.NewRequest(&healthv1.HealthCheckRequest{
		Service: req.Service,
	}))
	if err != nil {
		return nil, err
	}
	return &CheckResponse{
		Status: Status(res.Msg.GetStatus()), //nolint:gosec // Conversion is safe; Status and ServingStatus share the same value space.
	}, nil
}

// WatchEvent is a single update delivered by [Client.Watch]. Exactly one of
// Response or Err is set. An Err event, when delivered, is the final event on
// the channel.
type WatchEvent struct {
	Response *CheckResponse
	Err      error
}

// Watch streams health status changes for the requested service. Status
// updates are delivered on the returned channel. The channel closes when the
// stream ends, either because ctx is canceled, the server terminates the
// stream, or an error occurs. If the stream terminates with an error and ctx
// has not been canceled, the final value on the channel has its Err field
// set.
//
// Callers cancel ctx to stop the stream and release resources:
//
//	ctx, cancel := context.WithCancel(ctx)
//	defer cancel()
//	for event := range client.Watch(ctx, &CheckRequest{Service: svc}) {
//	    if event.Err != nil {
//	        // Handle error; this is the final event.
//	        break
//	    }
//	    fmt.Println(event.Response.Status)
//	}
func (c *Client) Watch(ctx context.Context, req *CheckRequest) <-chan WatchEvent {
	events := make(chan WatchEvent)
	go func() {
		defer close(events)
		send := func(event WatchEvent) bool {
			select {
			case <-ctx.Done():
				return false
			case events <- event:
				return true
			}
		}
		stream, err := c.watch.CallServerStream(ctx, connect.NewRequest(&healthv1.HealthCheckRequest{
			Service: req.Service,
		}))
		if err != nil {
			send(WatchEvent{Err: err})
			return
		}
		defer stream.Close()
		for stream.Receive() {
			resp := &CheckResponse{
				Status: Status(stream.Msg().GetStatus()), //nolint:gosec // Conversion is safe; Status and ServingStatus share the same value space.
			}
			if !send(WatchEvent{Response: resp}) {
				return
			}
		}
		if err := stream.Err(); err != nil {
			send(WatchEvent{Err: err})
		}
	}()
	return events
}
