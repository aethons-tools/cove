package main

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"google.golang.org/grpc"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
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

// squawksMux routes exactly "/squawks", "/squawks/targets", and
// "/squawks/commit" to squawksH, "/escalate" to escH, and "/end" and "/idle"
// to turnEndH, and "/alarms" and "/alarms/{name}" to alarmH; every other path goes
// to broker unchanged. Used to mount Jam's brokered ticket-squawk
// endpoint (its target-discovery and commit-cursor subpaths included) and its
// brokered escalation-category endpoint on the same cove-facing handler as
// the broker, without disturbing the broker's own routing.
func squawksMux(squawksH, escH, turnEndH, alarmH, broker http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/alarms" || strings.HasPrefix(r.URL.Path, "/alarms/") {
			alarmH.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/squawks", "/squawks/targets", "/squawks/commit":
			squawksH.ServeHTTP(w, r)
		case "/escalate":
			escH.ServeHTTP(w, r)
		case "/end", "/idle":
			turnEndH.ServeHTTP(w, r)
		default:
			broker.ServeHTTP(w, r)
		}
	})
}

// coveHTTPHandler builds the cove-facing HTTP handler: the broker, plus the
// intercom endpoints (/squawks and its subpaths) and /escalate whenever Jam
// has an intercom log (with or without a Requisitioner — a personal session needs
// the intercom too) or a Requisitioner (whose coves have always had /escalate; with
// no log their sends fail with a clean 503). With neither, the broker alone —
// plus GET /connector, which is always mounted.
func coveHTTPHandler(broker http.Handler, st jam.Store, sup *jam.Supervisor, lg intercom.Store, requisitioner bool, log *slog.Logger) http.Handler {
	// GET /connector (the identity's client env) and GET /context (its session
	// context, recompiled live) are always served, ahead of the broker's
	// destination routes.
	connH := jam.NewConnectorHandler(st, time.Now, log)
	ctxH := jam.NewContextHandler(st, sup, time.Now, log)
	withCoveEndpoints := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/connector":
				connH.ServeHTTP(w, r)
				return
			case "/context":
				ctxH.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	if lg == nil && !requisitioner {
		return withCoveEndpoints(broker)
	}
	// Pass lg as both the reader and the appender only when it's genuinely
	// non-nil: it is an intercom.Store interface value holding a real backend or
	// a true nil interface, so this is a plain nil check (no typed-nil hazard).
	var squawksH *jam.SquawksHandler
	if lg != nil {
		squawksH = jam.NewSquawksHandler(st, lg, lg, log)
	} else {
		squawksH = jam.NewSquawksHandler(st, nil, nil, log)
	}
	escH := jam.NewEscalateHandler(st, sup, log)
	log.Info("Jam messages: mounted", "path", "/squawks")
	turnEndH := jam.NewTurnEndHandler(st, sup, log)
	alarmH := jam.NewAlarmHandler(st, sup, log)
	log.Info("Jam escalate: mounted", "path", "/escalate")
	log.Info("Jam turn-end: mounted", "paths", "/end,/idle")
	log.Info("Jam alarms: mounted", "paths", "/alarms,/alarms/{name}")
	return withCoveEndpoints(squawksMux(squawksH, escH, turnEndH, alarmH, broker))
}
