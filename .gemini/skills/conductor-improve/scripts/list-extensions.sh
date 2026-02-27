#!/bin/bash
#
# List all Claude Code extensions (skills, commands, agents)
# Organized by scope (global vs project) and type
#
# Usage: list-extensions.sh [--project-path /path/to/project]

set -e

# Colors
CYAN='\033[0;36m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
DIM='\033[2m'
NC='\033[0m'

PROJECT_PATH=""

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --project-path)
      PROJECT_PATH="$2"
      shift 2
      ;;
    -h|--help)
      echo "Usage: $0 [--project-path /path/to/project]"
      echo ""
      echo "Lists all Claude Code extensions organized by scope and type."
      exit 0
      ;;
    *)
      PROJECT_PATH="$1"
      shift
      ;;
  esac
done

# Use current directory if no project path specified
if [[ -z "$PROJECT_PATH" ]]; then
  PROJECT_PATH="$(pwd)"
fi

echo -e "${CYAN}=== Extension Inventory ===${NC}"
echo ""

# Function to list items in a directory
list_items() {
  local dir="$1"
  local type="$2"
  local indent="$3"

  if [[ -d "$dir" ]]; then
    for item in "$dir"/*; do
      if [[ -d "$item" ]]; then
        local name=$(basename "$item")
        local skill_file="$item/SKILL.md"
        if [[ -f "$skill_file" ]]; then
          # Extract description from frontmatter
          local desc=$(grep -A1 "^description:" "$skill_file" 2>/dev/null | tail -1 | sed 's/description: //' | head -c 60)
          echo -e "${indent}${GREEN}/$name${NC} ${DIM}(skill)${NC}"
          if [[ -n "$desc" && "$desc" != "---" ]]; then
            echo -e "${indent}  ${DIM}$desc${NC}"
          fi
        fi
      elif [[ -f "$item" && "$item" == *.md ]]; then
        local name=$(basename "$item" .md)
        if [[ "$name" == *.agent ]]; then
          name=$(basename "$name" .agent)
          echo -e "${indent}${GREEN}$name${NC} ${DIM}(agent)${NC}"
        else
          echo -e "${indent}${GREEN}/$name${NC} ${DIM}(command)${NC}"
        fi
      fi
    done
  fi
}

# Global Extensions
echo -e "${YELLOW}## Global Extensions (~/.claude/)${NC}"
echo ""

echo -e "${CYAN}Skills:${NC}"
if [[ -d "$HOME/.claude/skills" ]]; then
  list_items "$HOME/.claude/skills" "skill" "  "
else
  echo "  (none)"
fi
echo ""

echo -e "${CYAN}Commands:${NC}"
if [[ -d "$HOME/.claude/commands" ]]; then
  # List commands (excluding agents and subdirectories)
  for item in "$HOME/.claude/commands"/*.md; do
    if [[ -f "$item" ]]; then
      local name=$(basename "$item" .md)
      if [[ "$name" != *.agent ]]; then
        echo -e "  ${GREEN}/$name${NC}"
      fi
    fi
  done 2>/dev/null || echo "  (none)"

  # List command subdirectories (like conductor/)
  for subdir in "$HOME/.claude/commands"/*/; do
    if [[ -d "$subdir" ]]; then
      local dirname=$(basename "$subdir")
      echo -e "  ${CYAN}$dirname/${NC}"
      for cmd in "$subdir"*.md; do
        if [[ -f "$cmd" ]]; then
          local name=$(basename "$cmd" .md)
          echo -e "    ${GREEN}/$dirname:$name${NC}"
        fi
      done 2>/dev/null
    fi
  done 2>/dev/null
else
  echo "  (none)"
fi
echo ""

echo -e "${CYAN}Agents:${NC}"
if [[ -d "$HOME/.claude/commands" ]]; then
  found_agents=false
  for item in "$HOME/.claude/commands"/*.agent.md; do
    if [[ -f "$item" ]]; then
      local name=$(basename "$item" .agent.md)
      echo -e "  ${GREEN}$name${NC}"
      found_agents=true
    fi
  done 2>/dev/null
  if [[ "$found_agents" == false ]]; then
    echo "  (none)"
  fi
else
  echo "  (none)"
fi
echo ""

# Project Extensions
echo -e "${YELLOW}## Project Extensions ($PROJECT_PATH/.claude/)${NC}"
echo ""

echo -e "${CYAN}Skills:${NC}"
if [[ -d "$PROJECT_PATH/.claude/skills" ]]; then
  list_items "$PROJECT_PATH/.claude/skills" "skill" "  "
else
  echo "  (none)"
fi
echo ""

echo -e "${CYAN}Commands:${NC}"
if [[ -d "$PROJECT_PATH/.claude/commands" ]]; then
  for item in "$PROJECT_PATH/.claude/commands"/*.md; do
    if [[ -f "$item" ]]; then
      local name=$(basename "$item" .md)
      if [[ "$name" != *.agent ]]; then
        echo -e "  ${GREEN}/$name${NC}"
      fi
    fi
  done 2>/dev/null || echo "  (none)"

  # List command subdirectories
  for subdir in "$PROJECT_PATH/.claude/commands"/*/; do
    if [[ -d "$subdir" ]]; then
      local dirname=$(basename "$subdir")
      echo -e "  ${CYAN}$dirname/${NC}"
      for cmd in "$subdir"*.md; do
        if [[ -f "$cmd" ]]; then
          local name=$(basename "$cmd" .md)
          echo -e "    ${GREEN}/$dirname:$name${NC}"
        fi
      done 2>/dev/null
    fi
  done 2>/dev/null
else
  echo "  (none)"
fi
echo ""

# Conductor extensions
if [[ -d "$PROJECT_PATH/conductor/extensions" ]]; then
  echo -e "${YELLOW}## Conductor Extensions ($PROJECT_PATH/conductor/extensions/)${NC}"
  echo ""
  list_items "$PROJECT_PATH/conductor/extensions" "extension" "  "
  echo ""
fi

# Summary
echo -e "${CYAN}=== Summary ===${NC}"
global_skills=$(find "$HOME/.claude/skills" -name "SKILL.md" 2>/dev/null | wc -l | tr -d ' ')
global_commands=$(find "$HOME/.claude/commands" -name "*.md" ! -name "*.agent.md" 2>/dev/null | wc -l | tr -d ' ')
global_agents=$(find "$HOME/.claude/commands" -name "*.agent.md" 2>/dev/null | wc -l | tr -d ' ')

project_skills=$(find "$PROJECT_PATH/.claude/skills" -name "SKILL.md" 2>/dev/null | wc -l | tr -d ' ')
project_commands=$(find "$PROJECT_PATH/.claude/commands" -name "*.md" ! -name "*.agent.md" 2>/dev/null | wc -l | tr -d ' ')

echo "Global: $global_skills skills, $global_commands commands, $global_agents agents"
echo "Project: $project_skills skills, $project_commands commands"
