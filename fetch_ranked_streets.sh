#!/bin/bash

set -o pipefail

# from https://www.halifax.ca/transportation/streets-sidewalks/road-safety/traffic-calming-for-safer-streets
# "Current list of Ranked Streets (potential future implementation)"

# uses pdftotext from xpdf, `brew install xpdf` on macOS

curl --fail -L -o street-calming-ranked-2020-11.pdf https://www.halifax.ca/media/71412
pdftotext -simple street-calming-ranked-2020-11.pdf - | ruby -lpe 'next unless $_ =~ /^\s+\d/; $_.sub!(/^\s+/, ""); $_.gsub!(/\s{2,}/, "\t")' | pbcopy

# needs manual massaging, get into street-calming-ranked-2020-11.tsv with header
# columns:
# Rank, Street Name, Limit From, Limit To, District
