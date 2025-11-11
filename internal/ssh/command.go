package ssh

import (
	"fmt"
)

// CommandBuilder constructs SSH command arguments consistently across the codebase.
// It ensures proper ordering of flags and handles optional parameters cleanly.
//
// Example usage:
//
//	args := ssh.NewCommand("ssh://user@host:2222", "/tmp/control.sock").
//	    WithControlOp("check").
//	    Build()
//	// Returns: ["-S", "/tmp/control.sock", "-p", "2222", "-O", "check", "user@host"]
type CommandBuilder struct {
	host        string
	port        string
	controlPath string
	controlOp   string   // check, exit, forward, cancel
	forwardSpec string   // for -L flag
	remoteCmd   string   // command to execute on remote host
	extraFlags  []string // any additional flags
}

// NewCommand creates a builder for SSH commands.
// The sshURL should be in format: ssh://user@host, ssh://user@host:port, or ssh://user@[::1]:port
// Returns error if URL format is invalid.
func NewCommand(sshURL, controlPath string) (*CommandBuilder, error) {
	host, port, err := ParseHost(sshURL)
	if err != nil {
		return nil, fmt.Errorf("invalid SSH URL: %w", err)
	}
	return &CommandBuilder{
		host:        host,
		port:        port,
		controlPath: controlPath,
	}, nil
}

// WithControlOp adds a control operation (-O flag).
// Valid operations: check, exit, forward, cancel
func (b *CommandBuilder) WithControlOp(op string) *CommandBuilder {
	b.controlOp = op
	return b
}

// WithPortForward adds a port forward specification (-L flag).
// Creates spec in format: 127.0.0.1:localPort:localhost:remotePort
func (b *CommandBuilder) WithPortForward(localPort, remotePort int) *CommandBuilder {
	b.forwardSpec = fmt.Sprintf("127.0.0.1:%d:localhost:%d", localPort, remotePort)
	return b
}

// WithRemoteCommand sets the command to execute on the remote host.
// This should be the final argument(s) to SSH.
func (b *CommandBuilder) WithRemoteCommand(cmd string) *CommandBuilder {
	b.remoteCmd = cmd
	return b
}

// WithExtraFlags adds arbitrary SSH flags (use sparingly).
// Useful for specialized cases not covered by other methods.
func (b *CommandBuilder) WithExtraFlags(flags ...string) *CommandBuilder {
	b.extraFlags = append(b.extraFlags, flags...)
	return b
}

// Build constructs the final SSH arguments array.
// Order is: -S <control> [-p <port>] [-O <op>] [-L <forward>] [extra] <host> [command]
func (b *CommandBuilder) Build() []string {
	args := make([]string, 0, 10)

	// Control socket is always first (required)
	args = append(args, "-S", b.controlPath)

	// Port flag (if specified)
	if b.port != "" {
		args = append(args, "-p", b.port)
	}

	// Control operation (if specified)
	if b.controlOp != "" {
		args = append(args, "-O", b.controlOp)
	}

	// Port forward specification (if specified)
	if b.forwardSpec != "" {
		args = append(args, "-L", b.forwardSpec)
	}

	// Extra flags (if any)
	args = append(args, b.extraFlags...)

	// Host is always required
	args = append(args, b.host)

	// Remote command is optional and comes last
	if b.remoteCmd != "" {
		args = append(args, b.remoteCmd)
	}

	return args
}
