#!/usr/bin/env bash
# Initialize a new ADR file from the MADR template.
#
# Usage: init-adr.sh <slug> <target-directory> <title>
#
# Creates <target-directory>/NNNN-<slug>.md from the ADR template,
# where NNNN is the next sequential 4-digit number in that directory.
#
# The slug must be lowercase letters, digits, and hyphens only,
# with no leading/trailing/consecutive hyphens, and max 64 characters.
#
# Substitutes NNNN with the assigned number, YYYY-MM-DD with today's date,
# and TITLE_PLACEHOLDER with the provided title.
#
# Outputs JSON with ADR_FILE, ADR_DIR, ADR_ID, and ADR_NUMBER on success.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TEMPLATE="$SCRIPT_DIR/../assets/adr-template.md"

usage() {
  echo "Usage: $(basename "$0") <slug> <target-directory> <title>"
  echo ""
  echo "Creates a new ADR file from the MADR template with auto-numbered ID."
  echo ""
  echo "Slug rules:"
  echo "  - Lowercase letters (a-z), digits (0-9), and hyphens (-) only"
  echo "  - No leading, trailing, or consecutive hyphens"
  echo "  - Maximum 64 characters"
  echo ""
  echo "Examples:"
  echo "  $(basename "$0") use-postgres-event-store docs/adr \"Use Postgres for the event store\""
  echo "  $(basename "$0") adopt-trunk-based-dev docs/decisions \"Adopt trunk-based development\""
}

if [ $# -ne 3 ]; then
  usage
  exit 1
fi

SLUG="$1"
TARGET_DIR="$2"
TITLE="$3"

# Validate slug
if [ ${#SLUG} -gt 64 ]; then
  echo "Error: Slug exceeds 64 characters (${#SLUG})."
  exit 1
fi

if ! echo "$SLUG" | grep -qE '^[a-z0-9-]+$'; then
  echo "Error: Slug must contain only lowercase letters, digits, and hyphens."
  echo "  Got: $SLUG"
  exit 1
fi

if echo "$SLUG" | grep -qE '(^-|-$|--)'; then
  echo "Error: Slug cannot start/end with a hyphen or contain consecutive hyphens."
  echo "  Got: $SLUG"
  exit 1
fi

if [ -z "$TITLE" ]; then
  echo "Error: Title must not be empty."
  exit 1
fi

if [ ! -f "$TEMPLATE" ]; then
  echo "Error: Template not found at $TEMPLATE"
  exit 1
fi

# Create target directory if needed
mkdir -p "$TARGET_DIR"

# Determine next ADR number by scanning existing NNNN-*.md files
MAX=0
shopt -s nullglob
for f in "$TARGET_DIR"/[0-9][0-9][0-9][0-9]-*.md; do
  base="$(basename "$f")"
  num="${base:0:4}"
  # Strip leading zeros for arithmetic; guard empty result
  num_int=$((10#$num))
  if [ "$num_int" -gt "$MAX" ]; then
    MAX=$num_int
  fi
done
shopt -u nullglob

NEXT=$((MAX + 1))
NEXT_PADDED=$(printf "%04d" "$NEXT")
ADR_ID="ADR-$NEXT_PADDED"

ADR_FILE="$TARGET_DIR/$NEXT_PADDED-$SLUG.md"

if [ -e "$ADR_FILE" ]; then
  echo "Error: $ADR_FILE already exists."
  exit 1
fi

TODAY=$(date +%Y-%m-%d)

# Escape slashes and ampersands for sed replacement of title
TITLE_ESCAPED=$(printf '%s' "$TITLE" | sed -e 's/[\/&|]/\\&/g')

# Substitute placeholders. Use | as sed delimiter to tolerate slashes in title.
sed \
  -e "s|ADR-NNNN|$ADR_ID|g" \
  -e "s|YYYY-MM-DD|$TODAY|g" \
  -e "s|TITLE_PLACEHOLDER|$TITLE_ESCAPED|g" \
  "$TEMPLATE" > "$ADR_FILE"

ADR_FILE_ABS="$(cd "$(dirname "$ADR_FILE")" && pwd)/$(basename "$ADR_FILE")"
ADR_DIR_ABS="$(cd "$TARGET_DIR" && pwd)"

jq -n \
  --arg f "$ADR_FILE_ABS" \
  --arg d "$ADR_DIR_ABS" \
  --arg id "$ADR_ID" \
  --arg n "$NEXT_PADDED" \
  '{ADR_FILE: $f, ADR_DIR: $d, ADR_ID: $id, ADR_NUMBER: $n}'
