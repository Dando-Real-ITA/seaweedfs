#!/usr/bin/env bash
# -*- coding: utf-8 -*-
# 2022-08-04 17:08:53

########################################################################################################################################################################################################################

# Check if file descriptor 6 is open
if [[ -n "$(ls /proc/$$/fd/ | grep 6)" ]]; then
  # Client

  # Send exported local ip and hostname
  echo "$LOCAL_IP $LOCAL_HOSTNAME" >&7

  # Receive remote ip and hostname
  read -u 6 REMOTE_IP REMOTE_HOSTNAME

  # Add/Update host file
  /hosts.sh add $REMOTE_IP $REMOTE_HOSTNAME >&2

  # Return remote hostname to calling script
  echo $REMOTE_HOSTNAME
else
  # Server

  # Receive remote ip and hostname
  read -u 0 REMOTE_IP REMOTE_HOSTNAME

  # Add/Update host file
  /hosts.sh add $REMOTE_IP $REMOTE_HOSTNAME >&2

  # Send exported local ip and hostname
  echo "$LOCAL_IP $LOCAL_HOSTNAME" >&1
fi

########################################################################################################################################################################################################################
