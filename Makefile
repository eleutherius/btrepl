BINARY  := btrepl
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.1")
ARCHS   := amd64 arm64

BUILD_DIR := build
DEB_MAINTAINER := Oleksandr Liakhov <eleutherius69@gmail.com>
DEB_DESCRIPTION := btrfs snapshot replication tool

GO_SOURCES := $(shell find . -name '*.go' -not -path './.git/*')

.PHONY: all build deb clean

all: deb

build: $(foreach arch,$(ARCHS),$(BUILD_DIR)/bin/$(BINARY)_linux_$(arch))

$(BUILD_DIR)/bin/$(BINARY)_linux_%: $(GO_SOURCES)
	GOOS=linux GOARCH=$* go build -ldflags="-s -w -X main.version=$(VERSION)" \
		-o $@ ./cmd/$(BINARY)

deb: build $(foreach arch,$(ARCHS),$(BUILD_DIR)/$(BINARY)_$(VERSION)_$(arch).deb)

$(BUILD_DIR)/$(BINARY)_$(VERSION)_%.deb: $(BUILD_DIR)/bin/$(BINARY)_linux_%
	$(eval PKG := $(BUILD_DIR)/pkg/$(BINARY)_$(VERSION)_$*)
	$(eval GOARCH := $*)
	@rm -rf $(PKG)

	@mkdir -p $(PKG)/usr/local/bin $(PKG)/lib/systemd/system $(PKG)/DEBIAN
	@install -m755 $< $(PKG)/usr/local/bin/$(BINARY)
	@install -m644 deploy/$(BINARY).service $(PKG)/lib/systemd/system/$(BINARY).service
	@install -m644 deploy/$(BINARY).timer    $(PKG)/lib/systemd/system/$(BINARY).timer

	@# DEBIAN/control
	@:
	@printf 'Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: %s\nDescription: %s\n' \
		"$(BINARY)" "$(VERSION)" "$(GOARCH)" "$(DEB_MAINTAINER)" "$(DEB_DESCRIPTION)" \
		> $(PKG)/DEBIAN/control

	@# DEBIAN/postinst
	@printf '#!/bin/sh\nset -e\nsystemctl daemon-reload\nsystemctl enable $(BINARY).timer\nsystemctl start $(BINARY).timer\n' \
		> $(PKG)/DEBIAN/postinst
	@chmod 755 $(PKG)/DEBIAN/postinst

	@# DEBIAN/prerm
	@printf '#!/bin/sh\nset -e\nsystemctl stop $(BINARY).timer || true\nsystemctl disable $(BINARY).timer || true\n' \
		> $(PKG)/DEBIAN/prerm
	@chmod 755 $(PKG)/DEBIAN/prerm

	dpkg-deb --build --root-owner-group $(PKG) $@
	@echo "built: $@"

clean:
	rm -rf $(BUILD_DIR)
