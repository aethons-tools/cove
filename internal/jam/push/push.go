// Package push decides Jam's Web Push notifications: who a new squawk buzzes
// (Recipients), what the notification says (Build), and whose open app
// suppresses it (Focus). It is pure logic over a Reader; delivery lives in
// the relay. See docs/superpowers/specs/2026-10-23-jam-push-notifications-design.md.
package push
