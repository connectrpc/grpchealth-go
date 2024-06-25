grpchealth
==========

[![Build](https://github.com/connectrpc/grpchealth-go/actions/workflows/ci.yaml/badge.svg?branch=main)](https://github.com/connectrpc/grpchealth-go/actions/workflows/ci.yaml)
[![Report Card](https://goreportcard.com/badge/connectrpc.com/grpchealth)](https://goreportcard.com/report/connectrpc.com/grpchealth)
[![GoDoc](https://pkg.go.dev/badge/connectrpc.com/grpchealth.svg)](https://pkg.go.dev/connectrpc.com/grpchealth)
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fconnectrpc%2Fgrpchealth-go.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Fconnectrpc%2Fgrpchealth-go?ref=badge_shield)

`connectrpc.com/grpchealth` adds support for gRPC-style health checks to any
`net/http` server &mdash; including those built with [Connect][connect]. By
polling this API, load balancers, container orchestrators, and other
infrastructure systems can respond to changes in your HTTP server's health.

The exposed health checking API is wire compatible with Google's gRPC
implementations, so it works with [grpcurl], [grpc-health-probe], and
[Kubernetes gRPC liveness probes][k8s-liveness].

For more on Connect, see the [announcement blog post][blog], the documentation
on [connectrpc.com][docs] (especially the [Getting Started] guide for Go), the
[Connect][connect] repo, or the [demo service][examples-go].

## Example

```go
package main

import (
  "net/http"

  "golang.org/x/net/http2"
  "golang.org/x/net/http2/h2c"
  "connectrpc.com/grpchealth"
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
  // If you don't need to support HTTP/2 without TLS (h2c), you can drop
  // x/net/http2 and use http.ListenAndServeTLS instead.
  http.ListenAndServe(
    ":8080",
    h2c.NewHandler(mux, &http2.Server{}),
  )
}
```

## Status: Stable

This module is stable. It supports:

* The three most recent major releases of Go. Keep in mind that [only the last
  two releases receive security patches][go-support-policy].
* [APIv2] of Protocol Buffers in Go (`google.golang.org/protobuf`).

Within those parameters, `grpchealth` follows semantic versioning.
We will _not_ make breaking changes in the 1.x series of releases.

## Legal

Offered under the [Apache 2 license][license].

[APIv2]: https://blog.golang.org/protobuf-apiv2
[Getting Started]: https://connectrpc.com/go/getting-started
[blog]: https://buf.build/blog/connect-a-better-grpc
[connect]: https://github.com/connectrpc/connect-go
[examples-go]: https://github.com/connectrpc/examples-go
[docs]: https://connectrpc.com
[go-support-policy]: https://golang.org/doc/devel/release#policy
[grpc-health-probe]: https://github.com/grpc-ecosystem/grpc-health-probe/
[grpcurl]: https://github.com/fullstorydev/grpcurl
[k8s-liveness]: https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/#define-a-grpc-liveness-probe
[license]: https://github.com/connectrpc/grpchealth-go/blob/main/LICENSE.txt


## License
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fconnectrpc%2Fgrpchealth-go.svg?type=large)](https://app.fossa.com/projects/git%2Bgithub.com%2Fconnectrpc%2Fgrpchealth-go?ref=badge_large)