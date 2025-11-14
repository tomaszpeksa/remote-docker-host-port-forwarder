package docker

// Label constants used by rdhpf for container identification and custom port mappings.
const (
	// LabelTestInfrastructure marks containers that should be skipped during reconciliation.
	// This is used to exclude test infrastructure (like SSH containers) from port forwarding.
	// Value should be "true" to mark a container.
	LabelTestInfrastructure = "rdhpf.test-infrastructure"

	// LabelForwardPrefix is the prefix for custom port mapping labels.
	// Format: rdhpf.forward.LOCAL_PORT=CONTAINER_PORT
	// Example: rdhpf.forward.8080=80 creates a forward from localhost:8080 to container:80
	//
	// This is an advanced feature primarily used in testing scenarios where
	// containers cannot publish ports to avoid conflicts. Must be explicitly enabled
	// via RDHPF_ENABLE_LABEL_PORTS environment variable.
	LabelForwardPrefix = "rdhpf.forward."
)
