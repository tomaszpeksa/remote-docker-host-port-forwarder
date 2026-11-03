# SSH server with Docker CLI for integration testing
# Uses host Docker daemon via socket mount (no Docker-in-Docker)
# This avoids iptables compatibility issues while providing real Docker access

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

# Install exact packages from the normal signed stable Alpine repository.
# Docker CLI 29.9.2-r0: aports 0b8392c4e3a24608418ffde2d77923591f359ee3.
# The owned fixture records the installed client/compiler/API provenance.
RUN apk add --no-cache openssh-server=10.3_p1-r1 docker-cli=29.9.2-r0

# Configure sshd for key-based authentication and allow port forwarding
RUN sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/' /etc/ssh/sshd_config && \
    sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config && \
    sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin no/' /etc/ssh/sshd_config && \
    sed -i 's/^AllowTcpForwarding no/AllowTcpForwarding yes/' /etc/ssh/sshd_config && \
    echo 'PermitOpen any' >> /etc/ssh/sshd_config && \
    echo 'GatewayPorts no' >> /etc/ssh/sshd_config

# This owned loopback fixture deliberately repeats failed authentication. Keep
# those controls independent; never apply this test setting to operator sshd.
RUN echo 'PerSourcePenalties no' >> /etc/ssh/sshd_config

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
