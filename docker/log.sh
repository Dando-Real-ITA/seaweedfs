#!/usr/bin/env bash
# -*- coding: utf-8 -*-
# 2021-02-25 09:03:18

########################################################################################################################################################################################################################

GC='\033[0;32m' #green color
RC='\033[0;31m' #red color
OC='\033[0;33m' #orange color
NC='\033[0m' #no color
IC='\033[0;37m' #input text
BC='\033[1m' #bold text
UC='\033[4m' #underline text

function now { dt=$(date '+%F %T.%3N'); echo "${dt}"; }
function successLog { echo -e "${GC}$(now) - $1${NC}"; }
function warningLog { echo -e "${OC}$(now) - $1${NC}"; }
function errorLog { echo -e "${RC}$(now) - $1${NC}"; }
function inputLog { printf "${IC}$(now) - $1${NC}"; }
function titleLog { echo -e "${BC}$(now) - $1${NC}"; }
function sectionLog { echo -e "$(now) - ${UC}$1${NC}"; } 
function log { echo -e "$(now) - $1"; }

########################################################################################################################################################################################################################

# # Examples
# successLog Success
# warningLog Warning
# errorLog Error
# DEFAULT=0; inputLog "Input [$DEFAULT]: "; read test; TEST=${test:=$DEFAULT}; echo $TEST
# titleLog Title
# sectionLog Section
# log Log

########################################################################################################################################################################################################################
