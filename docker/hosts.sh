#!/usr/bin/env bash
# -*- coding: utf-8 -*-
# 2022-08-04 16:32:48

########################################################################################################################################################################################################################

# PATH TO YOUR HOSTS FILE
ETC_HOSTS="/etc/hosts"
TMP_HOSTS="/tmp/hosts"

########################################################################################################################################################################################################################

yell() { echo "$0: $*" >&2; }
die() { yell "$*"; exit 111; }
try() { "$@" || die "cannot $*"; }

remove() {
  if [ -z "$1" ]; then
    die "Exiting... invalid arguments";
  fi

  # Hostname to add/remove.
  HOSTNAME=$1

  # TESTARE CON GREP REGEX
  if [[ -n "$(grep -E "\s+$HOSTNAME$" /etc/hosts)" ]]; then
    echo "$HOSTNAME Found in $ETC_HOSTS, Removing now...";
    try cp $ETC_HOSTS $TMP_HOSTS;
    try sed -i -r "/\s+$HOSTNAME/d" "$TMP_HOSTS";
    try cat $TMP_HOSTS > $ETC_HOSTS;
  else
    yell "$HOSTNAME was not found in your $ETC_HOSTS";
  fi
}

add() {
  if [ -z "$1" ] || [ -z "$2" ]; then
    die "Exiting... invalid arguments";
  fi

  # IP to add/remove.
  IP=$1
  # Hostname to add/remove.
  HOSTNAME=$2

  host_line="$IP $HOSTNAME"

  if [[ -n "$(grep -E "\s+$HOSTNAME$" /etc/hosts)" ]]; then
    echo "$HOSTNAME Found in $ETC_HOSTS, Replacing now...";
    try cp $ETC_HOSTS $TMP_HOSTS;
    try sed -i -r "s/^.*\s+$HOSTNAME.*$/$host_line/" "$TMP_HOSTS";
    try cat $TMP_HOSTS > $ETC_HOSTS;
  else
    echo "Adding $HOSTNAME to $ETC_HOSTS...";
    echo "$host_line" >> /etc/hosts
  fi

  added_line=$(grep -E "\s+$HOSTNAME$" /etc/hosts)
  if [[ -n "$added_line" ]]; then
    echo "$HOSTNAME was added succesfully: '$added_line'";
  else
    die "Failed to add $HOSTNAME";
  fi
}

########################################################################################################################################################################################################################

if [ -z "$1" ] || [ -z "$2" ]; then
  die "Exiting... invalid arguments";
fi

$@

########################################################################################################################################################################################################################
