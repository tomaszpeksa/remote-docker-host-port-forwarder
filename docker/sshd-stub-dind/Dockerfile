# SSH server with Docker CLI for integration testing
# Uses host Docker daemon via socket mount (no Docker-in-Docker)
# This avoids iptables compatibility issues while providing real Docker access

FROM alpine:3.19

# Install OpenSSH server and Docker CLI
RUN apk add --no-cache openssh-server docker-cli

# Generate SSH host keys
RUN ssh-keygen -A

# Configure sshd for key-based authentication and allow port forwarding
RUN sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/' /etc/ssh/sshd_config && \
    sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config && \
    sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin no/' /etc/ssh/sshd_config && \
    sed -i 's/^AllowTcpForwarding no/AllowTcpForwarding yes/' /etc/ssh/sshd_config && \
    echo 'PermitOpen any' >> /etc/ssh/sshd_config && \
    echo 'GatewayPorts no' >> /etc/ssh/sshd_config

# Create test user with password (required by Alpine sshd even with key auth)
RUN adduser -D -s /bin/sh testuser && \
    echo 'testuser:testpass' | chpasswd && \
    mkdir -p /home/testuser/.ssh && \
    chown testuser:testuser /home/testuser/.ssh && \
    chmod 700 /home/testuser/.ssh

# Create docker group with GID 970 (matches host docker group for socket access)
# Add testuser to docker group so they can access /var/run/docker.sock
RUN addgroup -g 970 docker && \
    addgroup testuser docker

EXPOSE 22

# Copy and configure entrypoint script
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Run entrypoint that configures localhost->gateway mapping then starts sshd
CMD ["/entrypoint.sh"]