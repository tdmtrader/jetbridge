# Setup Wizard Design Patterns

## The Standard Flow

Most setup wizards follow this pattern:

### Phase 1: Discovery
- Ask about pain points and goals
- Understand the user's current workflow
- Identify what they want to improve

### Phase 2: Source Inventory
- For each category of tools, ask which services they use
- Example: "Which email accounts do you use? (Gmail, Outlook, etc.)"
- Build a list of connected services

### Phase 3: Tool Check
- For each selected service, verify the CLI tool is installed
- Use SkillEligibilityService to check bins/env
- Guide installation if missing

### Phase 4: Authentication
- Walk through credential setup per service
- App passwords, API keys, OAuth flows
- Store securely in environment variables

### Phase 5: Preferences
- Working hours, time zone
- Priority rules (who matters most)
- Notification preferences

### Phase 6+: Generate Files
- Create conductor files (product.md, tech-stack.md, workflow.md)
- Generate personalized dashboard
- Set up persistent tracks with initial tasks
- Schedule recurring skills

## Tips

- Each phase should be conversational, not a form
- Let the user skip phases they don't need
- Provide sensible defaults
- Generate files at the end, not during the conversation
