#!/usr/bin/env bash
# -*- coding: utf-8 -*-
# 2022-11-15 15:06:12

########################################################################################################################################################################################################################

# Load log functions
. log.sh

exit_cleanup() {
  log "Cleanup"

  exit $1
}

########################################################################################################################################################################################################################

log "Importing ENV variables"
set -a; . build_docker.env; set +a

# INFO
titleLog "Building SeaweedFS ${VERSION}"

sectionLog "Building linux version"
cd ${BUILD_FOLDER}
make linux
if [[ $? -eq 0 ]]; then
  successLog "Builded linux version"
else
  errorLog "Failed to build linux version"
  exit_cleanup 1
fi

log "Exporting binaries"
cd linux/weed
tar -cvzf weed.tar.gz *
cp weed.tar.gz ${COPY_FOLDER}

log "Cleanup"
rm weed*
cd ../../

########################################################################################################################################################################################################################

sectionLog "Building SeaweedFS Docker images"
cd ${DOCKER_FOLDER}

DOCKER_BUILDKIT=1 docker build --pull \
  -f Dockerfile.go_build --target final \
  -t gasparekatapy/seaweedfs:${VERSION} \
  -t gasparekatapy/seaweedfs:latest \
  .
if [[ $? -eq 0 ]]; then
  successLog "Builded latest images"
else
  errorLog "Failed to build images"
  exit_cleanup 1
fi

DOCKER_BUILDKIT=1 docker build --pull \
  -f Dockerfile.go_build --target final_large \
  -t gasparekatapy/seaweedfs:large-${VERSION} \
  -t gasparekatapy/seaweedfs:large \
  .
if [[ $? -eq 0 ]]; then
  successLog "Builded latest large images"
else
  errorLog "Failed to build large images"
  exit_cleanup 1
fi

sectionLog "Pushing latest images"
docker push gasparekatapy/seaweedfs:${VERSION} \
&& docker push gasparekatapy/seaweedfs:latest \
&& docker push gasparekatapy/seaweedfs:large-${VERSION} \
&& docker push gasparekatapy/seaweedfs:large
if [[ $? -eq 0 ]]; then
  successLog "Pushed latest images"
else
  errorLog "Failed to push images"
  exit_cleanup 1
fi

########################################################################################################################################################################################################################

sectionLog "Building SeaweedFS Docker plugin"
sleep 120
cd ${PLUGIN_FOLDER}
make push
if [[ $? -eq 0 ]]; then
  successLog "Builded and pushed latest plugins"
else
  errorLog "Failed to build and push plugins"
  exit_cleanup 1
fi

exit_cleanup 0

########################################################################################################################################################################################################################
