# Minimal Alpine-based SSH server with docker shim for integration testing
FROM alpine:3.19

# Install OpenSSH server and utilities needed by the shim
RUN apk add --no-cache openssh-server jq bc sudo

# Generate SSH host keys
RUN ssh-keygen -A

# Configure sshd for key-based auth
RUN sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/' /etc/ssh/sshd_config && \
    sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config && \
    sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin no/' /etc/ssh/sshd_config

# Create test user
RUN adduser -D -s /bin/sh testuser && \
    echo 'testuser:testpass' | chpasswd && \
    mkdir -p /home/testuser/.ssh && \
    chown testuser:testuser /home/testuser/.ssh && \
    chmod 700 /home/testuser/.ssh

# Install docker shim (will be copied from build context)
COPY docker-shim/docker /usr/local/bin/docker
RUN chmod +x /usr/local/bin/docker

# Ensure shim is found before any real docker
ENV PATH=/usr/local/bin:$PATH

# Create scenarios directory
RUN mkdir -p /opt/rdhpf-scenarios/default

EXPOSE 22

# Run sshd in foreground
CMD ["/usr/sbin/sshd", "-D", "-e"]