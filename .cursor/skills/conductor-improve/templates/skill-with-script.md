# Skill Template: With Bash Script

Use this template when creating a skill that automates work Claude currently does manually.

## Directory Structure

```
.claude/skills/<skill-name>/
├── SKILL.md          # Main instructions (this file)
├── scripts/
│   └── main.sh       # Automation script
└── reference.md      # Optional: detailed docs (lazy-loaded)
```

## SKILL.md Template

````yaml
---
name: <skill-name>
description: <When to use this skill. Include keywords users would naturally say.>
argument-hint: "[required-arg] [--optional-flag]"
allowed-tools: Bash(*), Read, Grep, Glob
---

# <Skill Name>

<One-line description of what this skill does.>

## Quick Start

```bash
~/.claude/skills/<skill-name>/scripts/main.sh [args]
````

## When to Use

Use this skill when:

- <Specific trigger condition 1>
- <Specific trigger condition 2>

## Usage

### Basic Usage

```bash
~/.claude/skills/<skill-name>/scripts/main.sh "argument"
```

### With Options

```bash
~/.claude/skills/<skill-name>/scripts/main.sh \
  --option1 value \
  --option2 \
  "argument"
```

## Options

| Flag            | Description           | Default |
| --------------- | --------------------- | ------- |
| `-o, --output`  | Output file path      | stdout  |
| `-v, --verbose` | Enable verbose output | false   |
| `-h, --help`    | Show help             | -       |

## Examples

### Example 1: <Use Case>

```bash
~/.claude/skills/<skill-name>/scripts/main.sh "example input"
```

Output:

```
<expected output>
```

### Example 2: <Another Use Case>

```bash
~/.claude/skills/<skill-name>/scripts/main.sh --option "input"
```

## How It Works

1. <Step 1 of what the script does>
2. <Step 2>
3. <Step 3>

## Error Handling

The script handles these error cases:

- <Error case 1>: <What happens>
- <Error case 2>: <What happens>

## Requirements

- `jq` for JSON parsing (install: `brew install jq`)
- <Other requirements>

````

## Script Template (scripts/main.sh)

```bash
#!/bin/bash
#
# <Script description>
#
# Usage: main.sh [OPTIONS] <argument>

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# Default values
OUTPUT=""
VERBOSE=false
ARGUMENT=""

# Show usage
show_usage() {
  echo "Usage: $0 [OPTIONS] <argument>"
  echo ""
  echo "Options:"
  echo "  -o, --output FILE    Output to file instead of stdout"
  echo "  -v, --verbose        Enable verbose output"
  echo "  -h, --help           Show this help message"
  echo ""
  echo "Examples:"
  echo "  $0 \"input value\""
  echo "  $0 -o output.txt \"input value\""
  exit 1
}

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    -o|--output)
      OUTPUT="$2"
      shift 2
      ;;
    -v|--verbose)
      VERBOSE=true
      shift
      ;;
    -h|--help)
      show_usage
      ;;
    -*)
      echo -e "${RED}Error: Unknown option $1${NC}"
      show_usage
      ;;
    *)
      ARGUMENT="$1"
      shift
      ;;
  esac
done

# Validate required argument
if [[ -z "$ARGUMENT" ]]; then
  echo -e "${RED}Error: Argument required${NC}"
  show_usage
fi

# Verbose logging function
log() {
  if [[ "$VERBOSE" == true ]]; then
    echo -e "${CYAN}[INFO]${NC} $1"
  fi
}

# Main logic
main() {
  log "Processing: $ARGUMENT"

  # Your logic here
  result="Processed: $ARGUMENT"

  # Output
  if [[ -n "$OUTPUT" ]]; then
    echo "$result" > "$OUTPUT"
    echo -e "${GREEN}Output written to: $OUTPUT${NC}"
  else
    echo "$result"
  fi
}

# Run
main
````

## Making Script Executable

After creating the script:

```bash
chmod +x ~/.claude/skills/<skill-name>/scripts/main.sh
```

## Testing the Skill

Before marking the skill complete:

```bash
# Test basic usage
~/.claude/skills/<skill-name>/scripts/main.sh "test"

# Test with options
~/.claude/skills/<skill-name>/scripts/main.sh -v "test"

# Test error handling
~/.claude/skills/<skill-name>/scripts/main.sh  # Should show usage
```
