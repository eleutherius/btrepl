BINARY  := btrepl
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^[^0-9]/0.0.0+&/' || echo "0.0.1")
ARCHS   := amd64 arm64

GO_ROOT  := src/btrepl
BUILD_DIR := build
DEB_MAINTAINER := Oleksandr Liakhov <eleutherius69@gmail.com>
DEB_DESCRIPTION := btrfs snapshot replication tool

GO_SOURCES := $(shell find $(GO_ROOT) -name '*.go' -not -path './.git/*')
PROTO_SRC   := $(GO_ROOT)/api/btrepl.proto
PROTO_GO    := $(GO_ROOT)/api/btrepl.pb.go $(GO_ROOT)/api/btrepl_grpc.pb.go
PROTO_PY    := src/pybtrepl/btrepl/_pb/btrepl_pb2.py src/pybtrepl/btrepl/_pb/btrepl_pb2_grpc.py

.PHONY: all proto proto-go proto-py build deb clean

all: deb

proto: proto-go proto-py

proto-go: $(PROTO_GO)

$(PROTO_GO): $(PROTO_SRC)
	protoc --go_out=$(GO_ROOT) --go_opt=paths=source_relative \
	       --go-grpc_out=$(GO_ROOT) --go-grpc_opt=paths=source_relative \
	       --proto_path=$(GO_ROOT) $(PROTO_SRC)

proto-py: $(PROTO_PY)

$(PROTO_PY): $(PROTO_SRC)
	python3 -m grpc_tools.protoc \
	    --proto_path=$(GO_ROOT)/api \
	    --python_out=src/pybtrepl/btrepl/_pb \
	    --grpc_python_out=src/pybtrepl/btrepl/_pb \
	    --pyi_out=src/pybtrepl/btrepl/_pb \
	    $(PROTO_SRC)
	@sed -i '' 's/^import btrepl_pb2/from btrepl._pb import btrepl_pb2/' \
	    src/pybtrepl/btrepl/_pb/btrepl_pb2_grpc.py

build: $(foreach arch,$(ARCHS),$(BUILD_DIR)/bin/$(BINARY)_linux_$(arch))

$(BUILD_DIR)/bin/$(BINARY)_linux_%: $(GO_SOURCES)
	GOOS=linux GOARCH=$* go build -C $(GO_ROOT) \
		-ldflags="-s -w -X main.version=$(VERSION)" \
		-o ../../$@ ./cmd/$(BINARY)

deb: build $(foreach arch,$(ARCHS),$(BUILD_DIR)/$(BINARY)_$(VERSION)_$(arch).deb)

$(BUILD_DIR)/$(BINARY)_$(VERSION)_%.deb: $(BUILD_DIR)/bin/$(BINARY)_linux_%
	$(eval PKG := $(BUILD_DIR)/pkg/$(BINARY)_$(VERSION)_$*)
	$(eval GOARCH := $*)
	@rm -rf $(PKG)

	@mkdir -p $(PKG)/usr/local/bin $(PKG)/lib/systemd/system $(PKG)/DEBIAN
	@install -m755 $< $(PKG)/usr/local/bin/$(BINARY)
	@install -m644 $(GO_ROOT)/deploy/$(BINARY).service $(PKG)/lib/systemd/system/$(BINARY).service

	@printf 'Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: %s\nDescription: %s\n' \
		"$(BINARY)" "$(VERSION)" "$(GOARCH)" "$(DEB_MAINTAINER)" "$(DEB_DESCRIPTION)" \
		> $(PKG)/DEBIAN/control

	@printf '#!/bin/sh\nset -e\nsystemctl daemon-reload\nsystemctl enable $(BINARY).service\nsystemctl start $(BINARY).service\n' \
		> $(PKG)/DEBIAN/postinst
	@chmod 755 $(PKG)/DEBIAN/postinst

	@printf '#!/bin/sh\nset -e\nsystemctl stop $(BINARY).service || true\nsystemctl disable $(BINARY).service || true\n' \
		> $(PKG)/DEBIAN/prerm
	@chmod 755 $(PKG)/DEBIAN/prerm

	dpkg-deb --build --root-owner-group $(PKG) $@
	@echo "built: $@"

clean:
	rm -rf $(BUILD_DIR)
