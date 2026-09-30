package studio

// DefaultStudioKitID is the registry id the built-in default studio kit is
// seeded under. role.Kit == "" raises this kit.
const DefaultStudioKitID = "default"

// DefaultStudioKit is the built-in default a brokered cove runs when its role
// names no kit: the blessed base (empty Base → blessed default), a minimal
// Anthropic-free egress ceiling, and a generic orienting prompt. It is seeded
// into the registry at serve start (see jam.EnsureDefaultStudioKit) so it is
// inspectable and versioned like any other kit.
func DefaultStudioKit() StudioKit {
	return StudioKit{
		Kind:   Kind,
		Name:   DefaultStudioKitID,
		Egress: []string{"github.com", "pkg.go.dev"},
		Prompt: "You are a studio agent running in a brokered at-cove sandbox. " +
			"Follow the task you are given; reach external services only through the approved allow-list.",
	}
}
