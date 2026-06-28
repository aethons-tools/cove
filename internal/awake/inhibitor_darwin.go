//go:build darwin

package awake

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
)

// New returns an Inhibitor backed by macOS's caffeinate(8).
func New() Inhibitor { return caffeinate{} }

type caffeinate struct{}

// Inhibit starts `caffeinate -i -w <pid>`, which asserts against idle system
// sleep until killed. -i prevents idle system sleep (the display may still
// sleep). -w <pid> ties caffeinate's lifetime to cove's own pid as a crash
// safety-net; release also kills it explicitly for prompt teardown on a clean
// exit. The returned release is guarded by sync.Once so it is safe to call
// more than once.
func (caffeinate) Inhibit() (func(), error) {
	cmd := exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}, nil
}
