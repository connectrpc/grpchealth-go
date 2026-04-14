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
	"iter"
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

// Watch returns an iterator over health status changes for the requested
// service, and a stop function that cancels the underlying stream. The
// iterator yields a [*CheckResponse] and an error for each received message.
// When an error is yielded, it is the final value; the iterator terminates
// immediately after.
//
// Callers must defer the stop function to release the stream resources:
//
//	watchIter, stop := client.Watch(ctx, &CheckRequest{Service: svc})
//	defer stop()
//	for resp, err := range watchIter {
//	    if err != nil {
//	        // Handle error; this is the last iteration.
//	        break
//	    }
//	    fmt.Println(resp.Status)
//	}
func (c *Client) Watch(ctx context.Context, req *CheckRequest) (seq iter.Seq2[*CheckResponse, error], stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	return func(yield func(*CheckResponse, error) bool) {
		defer cancel()
		stream, err := c.watch.CallServerStream(ctx, connect.NewRequest(&healthv1.HealthCheckRequest{
			Service: req.Service,
		}))
		if err != nil {
			yield(nil, err)
			return
		}
		defer stream.Close()
		for stream.Receive() {
			resp := &CheckResponse{
				Status: Status(stream.Msg().GetStatus()), //nolint:gosec // Conversion is safe; Status and ServingStatus share the same value space.
			}
			if !yield(resp, nil) {
				return
			}
		}
		if err := stream.Err(); err != nil {
			yield(nil, err)
		}
	}, cancel
}
