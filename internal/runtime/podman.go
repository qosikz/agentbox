package runtime

// NewPodmanRunner returns a Runner backed by the podman CLI.
//
// Podman is CLI-compatible with every argument Andbo generates (including
// the hardening flags --cap-drop ALL and --security-opt no-new-privileges), so
// it reuses the shared containerRunner implementation: identical run
// semantics, secure defaults, and exit-code mapping as the docker runner, with
// only the engine binary swapped.
func NewPodmanRunner() Runner {
	return containerRunner{
		engine:         "podman",
		unavailableMsg: "podman is not available. Install Podman, switch to --engine docker, or use --dry-run.",
	}
}
