#!/bin/sh
# Installs or updates sepianod on the Raspberry Pi. Run as root from the folder
# holding sepianod and the files in deploy/ (deploy.sh does this over SSH).
set -eu
cd "$(dirname "$0")"

# A system user that may use the serial ports.
if ! id sepiano >/dev/null 2>&1; then
	useradd --system --home-dir /var/lib/sepiano --no-create-home \
		--shell /usr/sbin/nologin --groups dialout sepiano
fi
usermod -aG dialout sepiano

# Replace the binary atomically, so a running copy is never half-written.
install -m 0755 sepianod /usr/local/bin/sepianod.new
mv /usr/local/bin/sepianod.new /usr/local/bin/sepianod

install -m 0644 sepianod.service /etc/systemd/system/sepianod.service
install -m 0644 99-sepiano.rules /etc/udev/rules.d/99-sepiano.rules
if [ -d /etc/avahi/services ]; then
	install -m 0644 sepiano.avahi.service /etc/avahi/services/sepiano.service
fi
udevadm control --reload-rules
udevadm trigger --subsystem-match=tty --action=add

systemctl daemon-reload
systemctl enable sepianod >/dev/null 2>&1
systemctl restart sepianod
sleep 2
systemctl --no-pager --lines=8 status sepianod || true
