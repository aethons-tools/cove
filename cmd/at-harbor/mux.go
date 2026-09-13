package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"strings"

	"google.golang.org/grpc"
)

// serveMux serves both the broker (HTTP/1.1 and HTTP/2) and the Attach gRPC
// server on one TLS listener. A single native http.Server terminates TLS and
// negotiates h2/http1 per connection; requests with content-type
// application/grpc are handed to the gRPC server (grpc-go's ServeHTTP path),
// everything else to httpHandler (the broker). It blocks until lis closes.
func serveMux(lis net.Listener, tlsCfg *tls.Config, gs *grpc.Server, httpHandler http.Handler) error {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			gs.ServeHTTP(w, r)
			return
		}
		httpHandler.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: h, TLSConfig: tlsCfg}
	return srv.ServeTLS(lis, "", "")
}

// messagesMux routes exactly "/messages" to msgH; every other path goes to
// broker unchanged. Used to mount harbor's brokered ticket-messaging endpoint
// on the same cove-facing handler as the broker, without disturbing the
// broker's own routing.
func messagesMux(msgH, broker http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/messages" {
			msgH.ServeHTTP(w, r)
			return
		}
		broker.ServeHTTP(w, r)
	})
}
