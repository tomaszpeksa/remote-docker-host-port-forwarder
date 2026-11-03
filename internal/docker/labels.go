package docker

// Label constants used by rdhpf for container identification and host-port hints.
const (
	// LabelTestInfrastructure marks containers that should be skipped during reconciliation.
	// This is used to exclude test infrastructure (like SSH containers) from port forwarding.
	// Value should be "true" to mark a container.
	LabelTestInfrastructure = "rdhpf.test-infrastructure"

	// LabelForwardPrefix identifies opt-in same-number SSH-side localhost hints.
	// Example: rdhpf.forward.8080=80 targets localhost:8080; the value is ignored.
	// The service may be unrelated to the labeled container. The caller passes
	// resolved enablement explicitly; inspection never rereads environment.
	LabelForwardPrefix = "rdhpf.forward."
)
