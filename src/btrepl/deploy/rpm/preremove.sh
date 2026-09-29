#!/bin/sh
set -e
# $1 is 0 on erase and 1 on upgrade; only stop the service on erase.
if [ "$1" -eq 0 ]; then
    systemctl stop btrepl.service || true
    systemctl disable btrepl.service || true
fi
