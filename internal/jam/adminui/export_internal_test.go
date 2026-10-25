package adminui

import "github.com/aethons-tools/cove/internal/jam"

// SetConfigMarkStatesForTest swaps the page's drift computation (the
// external tests' seam); the returned func restores it.
func SetConfigMarkStatesForTest(f func(jam.Store) ([]jam.ConfigMarkState, error)) (restore func()) {
	prev := configMarkStates
	configMarkStates = f
	return func() { configMarkStates = prev }
}
