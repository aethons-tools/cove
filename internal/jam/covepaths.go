package jam

import "strings"

// CoveHandler names an endpoint Jam itself serves on the cove listener,
// ahead of the broker's destination routes. cmd/jam's cove mux dispatches by
// CoveHandlerFor, so this table is the one list of those paths, and no
// destination route may claim one (ReservedRoute).
type CoveHandler string

const (
	CoveConnector CoveHandler = "connector" // GET /connector
	CoveContext   CoveHandler = "context"   // GET /context
	CoveMetrics   CoveHandler = "metrics"   // /metrics
	CoveSquawks   CoveHandler = "squawks"   // /squawks and its subpaths
	CoveEscalate  CoveHandler = "escalate"  // /escalate
	CoveTurnEnd   CoveHandler = "turn-end"  // /end, /idle, /mute, /kept
	CoveReport    CoveHandler = "report"    // /report
	CoveMission   CoveHandler = "mission"   // /mission
	CoveFocus     CoveHandler = "focus"     // /focus
	CoveAlarms    CoveHandler = "alarms"    // /alarms, /alarms/{name}
	CoveExposures CoveHandler = "exposures" // /exposures, /exposures/{name}
)

// covePaths are the paths CoveHandlerFor serves: exact, or every path under
// one ending in "/".
var covePaths = []struct {
	path string
	h    CoveHandler
}{
	{"/connector", CoveConnector}, {"/context", CoveContext}, {"/metrics", CoveMetrics},
	{"/squawks", CoveSquawks}, {"/squawks/targets", CoveSquawks}, {"/squawks/commit", CoveSquawks},
	{"/squawks/call-in", CoveSquawks}, {"/squawks/leave", CoveSquawks}, {"/squawks/ask", CoveSquawks},
	{"/squawks/ask/amend", CoveSquawks}, {"/squawks/ask/withdraw", CoveSquawks}, {"/squawks/asks", CoveSquawks},
	{"/squawks/uploads", CoveSquawks}, {"/squawks/boards", CoveSquawks}, {"/squawks/boards/", CoveSquawks},
	{"/squawks/blobs/", CoveSquawks},
	{"/escalate", CoveEscalate},
	{"/end", CoveTurnEnd}, {"/idle", CoveTurnEnd}, {"/mute", CoveTurnEnd}, {"/kept", CoveTurnEnd},
	{"/report", CoveReport}, {"/mission", CoveMission}, {"/focus", CoveFocus},
	{"/alarms", CoveAlarms}, {"/alarms/", CoveAlarms},
	{"/exposures", CoveExposures}, {"/exposures/", CoveExposures},
}

// CoveHandlerFor names the Jam endpoint that serves reqPath on the cove
// listener; "" means the broker does.
func CoveHandlerFor(reqPath string) CoveHandler {
	for _, p := range covePaths {
		if reqPath == p.path || strings.HasSuffix(p.path, "/") && strings.HasPrefix(reqPath, p.path) {
			return p.h
		}
	}
	return ""
}

// ReservedRoute returns the path Jam serves that route would claim — one
// the route prefixes (so "/" claims them all), or one whose first segment the
// route is under (so "/squawks/x/" is refused too) — or "" when it claims
// none.
func ReservedRoute(route string) string {
	for _, p := range covePaths {
		root := "/" + strings.SplitN(strings.TrimPrefix(p.path, "/"), "/", 2)[0]
		if strings.HasPrefix(p.path, route) || route == root || strings.HasPrefix(route, root+"/") {
			return p.path
		}
	}
	return ""
}
