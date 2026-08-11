.PHONY: test admin-generate admin-build admin-clean admin-dev admin-run admin-test admin-fmt admin-help weed-commands

BINARY = weed
ADMIN_DIR = weed/admin

GO_FLAGS = #-v
SOURCE_DIR = ./weed/
debug ?= 0

appname := weed

install: admin-generate
	cd weed; go install

COMMIT ?= $(shell git rev-parse --short HEAD)
PRIVATE_VERSION ?= $(shell git describe --tags --abbrev=0)
LDFLAGS ?= -X github.com/seaweedfs/seaweedfs/weed/util.COMMIT=${COMMIT} -X github.com/seaweedfs/seaweedfs/weed/util.PRIVATE_VERSION=${PRIVATE_VERSION}
weed-commands:
	cd weed && $(MAKE) weed-db weed-sql

warp_install:
	go install github.com/minio/warp@v0.7.6

build = CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) go build -ldflags "-s -w -extldflags -static $(LDFLAGS)" -o build/$(appname)$(3) $(SOURCE_DIR)
tar = cd build && tar -cvzf $(1)_$(2).tar.gz $(appname)$(3) && rm $(appname)$(3)
zip = cd build && zip $(1)_$(2).zip $(appname)$(3) && rm $(appname)$(3)
full_install: admin-generate
	cd weed; go install -tags "elastic gocdk sqlite ydb tarantool tikv rclone"

build_large = CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) go build -tags 5BytesOffset -ldflags "-s -w -extldflags -static $(LDFLAGS)" -o build/$(appname)$(3) $(SOURCE_DIR)
tar_large = cd build && tar -cvzf $(1)_$(2)_large_disk.tar.gz $(appname)$(3) && rm $(appname)$(3)
zip_large = cd build && zip $(1)_$(2)_large_disk.zip $(appname)$(3) && rm $(appname)$(3)
server: install
	weed -v 0 server -s3 -filer -filer.maxMB=64 -volume.max=0 -master.volumeSizeLimitMB=100 -volume.preStopSeconds=1 -s3.port=8000 -s3.allowDeleteBucketNotEmpty=true -s3.config=./docker/compose/s3.json -metricsPort=9324

benchmark: install warp_install
	pkill weed || true
	pkill warp || true
	weed server -debug=$(debug) -s3 -filer -volume.max=0 -master.volumeSizeLimitMB=100 -volume.preStopSeconds=1 -s3.port=8000 -s3.allowDeleteBucketNotEmpty=false -s3.config=./docker/compose/s3.json &
	warp client &
	while ! nc -z localhost 8000 ; do sleep 1 ; done
	warp mixed --host=127.0.0.1:8000 --access-key=some_access_key1 --secret-key=some_secret_key1 --autoterm
	pkill warp
	pkill weed

.PHONY : clean deps build linux release windows_build darwin_build linux_build bsd_build clean

test: admin-generate
	cd weed; go test -tags "elastic gocdk sqlite ydb tarantool tikv rclone" -v ./...

# Admin component targets
admin-generate:
	@cd $(ADMIN_DIR) && $(MAKE) generate

admin-build: admin-generate
	@echo "Building admin component..."
	@cd $(ADMIN_DIR) && $(MAKE) build

admin-clean:
	@echo "Cleaning admin component..."
	@cd $(ADMIN_DIR) && $(MAKE) clean

admin-dev:
	@echo "Starting admin development server..."
	@cd $(ADMIN_DIR) && $(MAKE) dev

admin-run:
	@echo "Running admin server..."
	@cd $(ADMIN_DIR) && $(MAKE) run

admin-test:
	@echo "Testing admin component..."
	@cd $(ADMIN_DIR) && $(MAKE) test

admin-fmt:
	@echo "Formatting admin component..."
	@cd $(ADMIN_DIR) && $(MAKE) fmt

admin-help:
	@echo "Admin component help..."
	@cd $(ADMIN_DIR) && $(MAKE) help
