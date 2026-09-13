package main

import (
	"crypto/tls"
	"net"
	"net/http"

	"github.com/soheilhy/cmux"
	"google.golang.org/grpc"
)

// serveMux terminates TLS on lis, then multiplexes the decrypted stream onto two
// servers: requests with content-type application/grpc go to gs (the Attach gRPC
// server), everything else to httpHandler (the broker). TLS is terminated here,
// so gs must use default (insecure) server creds. It blocks until lis closes.
func serveMux(lis net.Listener, tlsCfg *tls.Config, gs *grpc.Server, httpHandler http.Handler) error {
	tlsLis := tls.NewListener(lis, tlsCfg)
	m := cmux.New(tlsLis)
	grpcL := m.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings("content-type", "application/grpc"))
	httpL := m.Match(cmux.Any())
	go func() { _ = gs.Serve(grpcL) }()
	go func() { _ = (&http.Server{Handler: httpHandler}).Serve(httpL) }()
	return m.Serve()
}
