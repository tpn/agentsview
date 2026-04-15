FROM alpine:3.21
RUN apk add --no-cache openssh-server tar && \
    ssh-keygen -A && \
    adduser -D -s /bin/sh testuser && \
    passwd -u testuser && \
    mkdir -p /home/testuser/.ssh && \
    ssh-keygen -t ed25519 -f /home/testuser/.ssh/id_test -N "" && \
    cat /home/testuser/.ssh/id_test.pub \
        >> /home/testuser/.ssh/authorized_keys && \
    chmod 700 /home/testuser/.ssh && \
    chmod 600 /home/testuser/.ssh/authorized_keys && \
    chown -R testuser:testuser /home/testuser/.ssh && \
    cp /home/testuser/.ssh/id_test /tmp/test_ssh_key && \
    chmod 644 /tmp/test_ssh_key
# Configure sshd: enable pubkey auth explicitly and allow
# login for testuser. Write a fresh config to avoid any
# Alpine defaults that might interfere.
RUN printf '%s\n' \
    "Port 22" \
    "PubkeyAuthentication yes" \
    "AuthorizedKeysFile .ssh/authorized_keys" \
    "PasswordAuthentication no" \
    "PermitRootLogin no" \
    "Subsystem sftp /usr/lib/ssh/sftp-server" \
    > /etc/ssh/sshd_config

# Claude session fixture: file must be <project>/<uuid>.jsonl
# (DiscoverClaudeProjects looks for .jsonl files directly in
# project dirs, not inside subdirectories)
RUN mkdir -p /home/testuser/.claude/projects/test-project
COPY testdata/ssh/session.jsonl \
    /home/testuser/.claude/projects/test-project/aaaaaaaa-1111-2222-3333-444444444444.jsonl
RUN chown -R testuser:testuser /home/testuser/.claude

EXPOSE 22
CMD ["/usr/sbin/sshd", "-D", "-e"]
