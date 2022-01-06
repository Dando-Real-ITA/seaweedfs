#!/usr/bin/env bash
# -*- coding: utf-8 -*-
# 2022-01-06 14:12:40

########################################################################################################################################################################################################################

echo_pretty() {
  echo -e "\n######################################################################"
  echo "$1"
  echo -e "######################################################################\n"
}

########################################################################################################################################################################################################################

echo_pretty "Importing ENV variables"
set -a; . build_docker.env; set +a

echo_pretty "Building FS"
cd ${BUILD_FOLDER}
make linux

echo_pretty "Exporting binaries"
cd linux/weed
tar -cvzf weed.tar.gz *
cp weed.tar.gz ${COPY_FOLDER}

echo_pretty "Cleanup"
rm weed*
cd ../../

########################################################################################################################################################################################################################

echo_pretty "Building FS Docker images"
cd ${DOCKER_FOLDER}

DOCKER_BUILDKIT=1 docker build --pull \
  -f Dockerfile.go_build --target final \
  -t gasparekatapy/seaweedfs:${VERSION} \
  -t gasparekatapy/seaweedfs:latest \
  .

DOCKER_BUILDKIT=1 docker build --pull \
  -f Dockerfile.go_build --target final_large \
  -t gasparekatapy/seaweedfs:large-${VERSION} \
  -t gasparekatapy/seaweedfs:large \
  .

echo_pretty "Pushing images"
docker push gasparekatapy/seaweedfs:${VERSION} \
&& docker push gasparekatapy/seaweedfs:latest \
&& docker push gasparekatapy/seaweedfs:large-${VERSION} \
&& docker push gasparekatapy/seaweedfs:large

########################################################################################################################################################################################################################

echo_pretty "Building FS Docker plugin"
sleep 120
cd ${PLUGIN_FOLDER}
make push

########################################################################################################################################################################################################################
