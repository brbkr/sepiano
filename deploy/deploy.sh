#!/bin/sh
# Cross-compiles sepianod for the Pi (linux/arm64), copies it with the deploy
# files over SSH and installs it with sudo.
#
#   deploy/deploy.sh [host]        # host defaults to "pi" (an ~/.ssh/config alias)
#
# On Windows, Git Bash's ssh cannot use keys held by the Windows ssh-agent;
# point SSH and SCP at Windows' own OpenSSH:
#   SSH=/c/Windows/System32/OpenSSH/ssh.exe SCP=/c/Windows/System32/OpenSSH/scp.exe deploy/deploy.sh
set -eu
HOST=${1:-pi}
SSH=${SSH:-ssh}
SCP=${SCP:-scp}
cd "$(dirname "$0")/.."

VERSION=$(git describe --always --dirty 2>/dev/null || echo dev)
mkdir -p dist
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
	-ldflags="-s -w -X main.version=$VERSION" -o dist/sepianod ./cmd/sepianod
echo "built sepianod $VERSION"

"$SSH" "$HOST" 'rm -rf /tmp/sepiano-deploy && mkdir -p /tmp/sepiano-deploy'
"$SCP" -q dist/sepianod deploy/install.sh deploy/sepianod.service \
	deploy/99-sepiano.rules deploy/sepiano.avahi.service "$HOST:/tmp/sepiano-deploy/"
"$SSH" "$HOST" 'sudo sh /tmp/sepiano-deploy/install.sh && rm -rf /tmp/sepiano-deploy'
