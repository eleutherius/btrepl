#!/bin/sh
set -e
systemctl daemon-reload
systemctl enable btrepl.service
systemctl start btrepl.service
