// Package sbx builds the argument vectors for invoking the sbx CLI.
// These functions are pure: they perform no I/O and return the args
// that follow the "sbx" binary name.
package sbx

// Pack returns the argv for packing a staged kit directory into a zip.
func Pack(stagingDir, zipPath string) []string {
	return []string{"kit", "pack", stagingDir, "-o", zipPath}
}

// CreateRun returns the argv for starting a new sandbox from a packed kit.
// The agent name is hardcoded to "claude". When volumes is empty, the
// current directory (".") is used as the single volume.
func CreateRun(name, zipPath string, volumes []string) []string {
	if len(volumes) == 0 {
		volumes = []string{"."}
	}
	args := []string{"run", "--name", name, "--kit", zipPath, "claude"}
	return append(args, volumes...)
}

// Run returns the argv for running an existing sandbox by name.
func Run(name string) []string {
	return []string{"run", name}
}

// Remove returns the argv for removing a sandbox by name.
func Remove(name string) []string {
	return []string{"remove", name}
}
