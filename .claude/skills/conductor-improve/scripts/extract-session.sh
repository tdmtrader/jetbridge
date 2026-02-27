#!/bin/bash
#
# Extract and analyze a Claude Code session for friction patterns
#
# Usage: extract-session.sh <session-id> [--account <account-id>]
#
# Outputs to /tmp/session-analysis/:
#   - user-messages.txt
#   - friction-patterns.txt
#   - tool-errors.txt
#   - summary.txt

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

SESSION_ID=""
ACCOUNT_ID=""
OUTPUT_DIR="/tmp/session-analysis"

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --account)
      ACCOUNT_ID="$2"
      shift 2
      ;;
    -h|--help)
      echo "Usage: $0 <session-id> [--account <account-id>]"
      echo ""
      echo "Extracts and analyzes a Claude Code session for friction patterns."
      echo ""
      echo "Options:"
      echo "  --account ID    Specify account ID for multi-account setups"
      echo "  -h, --help      Show this help"
      echo ""
      echo "Output: /tmp/session-analysis/"
      exit 0
      ;;
    -*)
      echo -e "${RED}Unknown option: $1${NC}"
      exit 1
      ;;
    *)
      SESSION_ID="$1"
      shift
      ;;
  esac
done

if [[ -z "$SESSION_ID" ]]; then
  echo -e "${RED}Error: Session ID required${NC}"
  echo "Usage: $0 <session-id>"
  exit 1
fi

# Find session file
find_session_file() {
  local session_id="$1"
  local found=""

  # Search patterns in order of priority
  local search_paths=(
    # Multi-account locations
    "$HOME/.conductor/accounts/*/claude_config/projects/*/${session_id}.jsonl"
    # Default Claude location
    "$HOME/.claude/projects/*/${session_id}.jsonl"
    # Legacy conductor location
    "$HOME/.conductor/claude_projects/*/${session_id}.jsonl"
  )

  for pattern in "${search_paths[@]}"; do
    # Use find to handle glob expansion safely
    while IFS= read -r -d '' file; do
      if [[ -f "$file" ]]; then
        found="$file"
        break 2
      fi
    done < <(find $(dirname "$pattern") -maxdepth 2 -name "$(basename "$pattern")" -print0 2>/dev/null || true)
  done

  echo "$found"
}

echo -e "${CYAN}=== Session Extraction ===${NC}"
echo "Session ID: $SESSION_ID"

# Find the session file
SESSION_FILE=$(find_session_file "$SESSION_ID")

if [[ -z "$SESSION_FILE" || ! -f "$SESSION_FILE" ]]; then
  echo -e "${RED}Error: Session file not found for ID: $SESSION_ID${NC}"
  echo ""
  echo "Searched locations:"
  echo "  - ~/.conductor/accounts/*/claude_config/projects/*/"
  echo "  - ~/.claude/projects/*/"
  echo "  - ~/.conductor/claude_projects/*/"
  exit 1
fi

echo -e "${GREEN}Found: $SESSION_FILE${NC}"
FILE_SIZE=$(du -h "$SESSION_FILE" | cut -f1)
echo "Size: $FILE_SIZE"

# Create output directory
mkdir -p "$OUTPUT_DIR"
rm -f "$OUTPUT_DIR"/*.txt

# Check for jq
if ! command -v jq &> /dev/null; then
  echo -e "${YELLOW}Warning: jq not installed. Using basic extraction.${NC}"
  echo "Install jq for better parsing: brew install jq"

  # Basic extraction without jq
  grep '"type":"user"' "$SESSION_FILE" | head -100 > "$OUTPUT_DIR/user-messages-raw.txt"
  echo "Extracted raw user messages (first 100)"
  exit 0
fi

echo ""
echo -e "${CYAN}Extracting user messages...${NC}"

# Extract user messages with jq (streaming for large files)
jq -r 'select(.type == "user") | .message.content // empty' "$SESSION_FILE" 2>/dev/null | \
  grep -v '^\[' | \
  grep -v '^{' | \
  grep -v '^$' | \
  head -200 > "$OUTPUT_DIR/user-messages.txt"

USER_COUNT=$(wc -l < "$OUTPUT_DIR/user-messages.txt" | tr -d ' ')
echo -e "${GREEN}Extracted $USER_COUNT user messages${NC}"

echo ""
echo -e "${CYAN}Detecting friction patterns...${NC}"

# Friction pattern detection
FRICTION_FILE="$OUTPUT_DIR/friction-patterns.txt"
echo "# Friction Patterns Detected" > "$FRICTION_FILE"
echo "# Session: $SESSION_ID" >> "$FRICTION_FILE"
echo "# Date: $(date)" >> "$FRICTION_FILE"
echo "" >> "$FRICTION_FILE"

# Corrections
echo "## Corrections" >> "$FRICTION_FILE"
grep -i -n "no,\|actually,\|that's not\|not what I\|let me rephrase\|incorrect" "$OUTPUT_DIR/user-messages.txt" 2>/dev/null >> "$FRICTION_FILE" || echo "(none found)" >> "$FRICTION_FILE"
echo "" >> "$FRICTION_FILE"

# Frustration
echo "## Frustration" >> "$FRICTION_FILE"
grep -i -n "frustrat\|not working\|still not\|didn't work\|doesn't work\|again?" "$OUTPUT_DIR/user-messages.txt" 2>/dev/null >> "$FRICTION_FILE" || echo "(none found)" >> "$FRICTION_FILE"
echo "" >> "$FRICTION_FILE"

# Repeated attempts
echo "## Repeated Attempts" >> "$FRICTION_FILE"
grep -i -n "try again\|one more\|retry\|let's try\|attempt" "$OUTPUT_DIR/user-messages.txt" 2>/dev/null >> "$FRICTION_FILE" || echo "(none found)" >> "$FRICTION_FILE"
echo "" >> "$FRICTION_FILE"

# Workarounds
echo "## Workarounds" >> "$FRICTION_FILE"
grep -i -n "workaround\|manually\|by hand\|alternative\|different approach\|instead" "$OUTPUT_DIR/user-messages.txt" 2>/dev/null >> "$FRICTION_FILE" || echo "(none found)" >> "$FRICTION_FILE"
echo "" >> "$FRICTION_FILE"

# Context loss (session compaction)
echo "## Context Loss" >> "$FRICTION_FILE"
grep -i -n "where we left\|lost our session\|continued from\|out of context\|what were we" "$OUTPUT_DIR/user-messages.txt" 2>/dev/null >> "$FRICTION_FILE" || echo "(none found)" >> "$FRICTION_FILE"

echo -e "${GREEN}Friction patterns saved to $FRICTION_FILE${NC}"

echo ""
echo -e "${CYAN}Extracting tool errors...${NC}"

# Extract tool errors
jq -r 'select(.type == "tool_result" and .isError == true) | "\(.toolName): \(.error // .content)"' "$SESSION_FILE" 2>/dev/null | \
  head -50 > "$OUTPUT_DIR/tool-errors.txt" || true

ERROR_COUNT=$(wc -l < "$OUTPUT_DIR/tool-errors.txt" | tr -d ' ')
echo -e "${GREEN}Found $ERROR_COUNT tool errors${NC}"

# Generate summary
echo ""
echo -e "${CYAN}Generating summary...${NC}"

SUMMARY_FILE="$OUTPUT_DIR/summary.txt"
cat > "$SUMMARY_FILE" << EOF
# Session Analysis Summary

Session ID: $SESSION_ID
File: $SESSION_FILE
Size: $FILE_SIZE
Analyzed: $(date)

## Statistics
- User messages: $USER_COUNT
- Tool errors: $ERROR_COUNT

## Friction Categories
$(grep -c "^[0-9]" "$OUTPUT_DIR/friction-patterns.txt" 2>/dev/null || echo "0") total friction indicators found

## Files Generated
- $OUTPUT_DIR/user-messages.txt
- $OUTPUT_DIR/friction-patterns.txt
- $OUTPUT_DIR/tool-errors.txt
- $OUTPUT_DIR/summary.txt

## Next Steps
1. Review friction-patterns.txt for improvement opportunities
2. Run: cat $OUTPUT_DIR/friction-patterns.txt
3. Use /conductor:improve to generate proposals
EOF

echo -e "${GREEN}Summary saved to $SUMMARY_FILE${NC}"

echo ""
echo -e "${GREEN}=== Extraction Complete ===${NC}"
echo ""
echo "Output directory: $OUTPUT_DIR"
echo ""
echo "Quick view commands:"
echo "  cat $OUTPUT_DIR/summary.txt"
echo "  cat $OUTPUT_DIR/friction-patterns.txt"
echo "  cat $OUTPUT_DIR/user-messages.txt | head -50"
