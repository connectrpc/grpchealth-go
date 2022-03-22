connect-grpchealth-go
=====================

[![Build](https://connectrpc.com/grpchealth/actions/workflows/ci.yml/badge.svg?event=push?branch=main)](https://connectrpc.com/grpchealth/actions/workflows/ci.yml)
[![Report Card](https://goreportcard.com/badge/connectrpc.com/grpchealth)](https://goreportcard.com/report/connectrpc.com/grpchealth)
[![GoDoc](https://pkg.go.dev/badge/connectrpc.com/grpchealth.svg)](https://pkg.go.dev/connectrpc.com/grpchealth)

`connect-grpchealth-go` lets any `net/http` server (including ones built with
[connect][docs]!) support gRPC-style health checks. The exposed health-checking
API is wire-compatible with Google's gRPC implementations, so it works with
[grpcurl][], [grpc-health-probe][], and [Kubernetes gRPC liveness
probes][k8s-liveness].

## Example

```go
package main

import (
  "net/http"

  grpchealth "connectrpc.com/grpchealth"
)

func main() {
  mux := http.NewServeMux()
  checker := grpchealth.NewStaticChecker(
    "acme.user.v1.UserService",
    "acme.group.v1.GroupService",
    // protoc-gen-connect-go generates package-level constants
    // for these fully-qualified protobuf service names, so you'd more likely
    // reference userv1.UserServiceName and groupv1.GroupServiceName.
  )
  mux.Handle(grpchealth.NewHandler(checker))
  http.ListenAndServeTLS(":8081", "server.crt", "server.key", mux)
}
```

## Status

Like [connect][] itself, connect-grpchealth-go is in _beta_. We plan to tag a
release candidate in July 2022 and stable v1 soon after the Go 1.19 release.

## Support and Versioning

connect-grpchealth-go supports:

* The [two most recent major releases][go-support-policy] of Go, with a minimum
  of Go 1.18.
* [APIv2][] of protocol buffers in Go (`google.golang.org/protobuf`).

Within those parameters, it follows semantic versioning.

## Legal

Offered under the [Apache 2 license][license].

[APIv2]: https://blog.golang.org/protobuf-apiv2
[connect]: https://github.com/bufbuild/connect
[docs]: https://bufconnect.com
[go-support-policy]: https://golang.org/doc/devel/release#policy
[grpc-health-probe]: https://github.com/grpc-ecosystem/grpc-health-probe/
[grpcurl]: https://github.com/fullstorydev/grpcurl
[k8s-liveness]: https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/#define-a-grpc-liveness-probe
[license]: https://connectrpc.com/grpchealth/blob/main/LICENSE.txt
