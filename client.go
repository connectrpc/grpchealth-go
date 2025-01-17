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
	"sync/atomic"

	"connectrpc.com/connect"
	healthv1 "connectrpc.com/grpchealth/internal/gen/go/connectext/grpc/health/v1"
)

// Client is a health-check client, for querying the health of a server.
type Client interface {
	// Check checks the health of the given service or the server as a whole
	// if the given service is the empty string.
	Check(ctx context.Context, service string) (Status, error)

	// Watch checks the health of the given service or the server as a whole
	// if the given service is the empty string and continues to receive
	// updates until the returned stop function is called.
	//
	// Callers must arrange for the given stop function to be called in the
	// future (possibly via defer) since failing to call it may leak resources.
	//
	// The results are queried via the returned channel. The channel is
	// closed when the watch operation terminates, either due to an error
	// or due to the stop function being called. The stop function can also be
	// used to recover the error that caused the operation to terminate. That
	// error will be nil if the stop function being called is why the operation
	// terminated or if the server terminated the operation without any error
	// code. If the given context times out or is canceled, the returned error
	// will wrap [context.Canceled] or [context.DeadlineExceeded]. Otherwise, a
	// non-nil error is an error code sent by the server when it terminated the
	// operation.
	Watch(ctx context.Context, service string) (results chan<- Status, stop func() error, err error)
}

// NewClient returns a new client that issues health check RPCs using the given
// HTTP client, base URL, and Connect options.
func NewClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) Client {
	return &client{
		connectClient: connect.NewClient[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse](
			httpClient, baseURL, opts...,
		),
	}
}

type client struct {
	connectClient *connect.Client[healthv1.HealthCheckRequest, healthv1.HealthCheckResponse]
}

func (c *client) Check(ctx context.Context, service string) (Status, error) {
	resp, err := c.connectClient.CallUnary(ctx, connect.NewRequest(&healthv1.HealthCheckRequest{Service: service}))
	if err != nil {
		return 0, err
	}
	return Status(resp.Msg.Status), nil
}

func (c *client) Watch(ctx context.Context, service string) (results chan<- Status, stop func() error, err error) {
	ctx, cancel := context.WithCancel(ctx)
	results = make(chan Status, 1)
	stream, err := c.connectClient.CallServerStream(ctx, connect.NewRequest(&healthv1.HealthCheckRequest{Service: service}))
	if err != nil {
		close(results)
		cancel()
		return results, func() error { return err }, err
	}

	var stopped atomic.Bool
	var recvError atomic.Value
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		defer close(results)
		defer cancel()
		for {
			if !stream.Receive() {
				err := stream.Err()
				closeErr := stream.Close()
				if err != nil && (!errors.Is(err, context.Canceled) || !stopped.Load()) {
					recvError.Store(err)
				} else if closeErr != nil {
					recvError.Store(closeErr)
				}
				return
			}
			select {
			case results <- Status(stream.Msg().Status):
			case <-ctx.Done():
				if err := ctx.Err(); !errors.Is(err, context.Canceled) || !stopped.Load() {
					recvError.Store(err)
				}
				return
			}
		}
	}()

	stop = func() (err error) {
		stopped.Store(true)
		cancel()
		<-workerDone
		recvErr, _ := recvError.Load().(error)
		return recvErr
	}

	return results, stop, nil
}
