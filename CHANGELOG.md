## Changelog

All notable changes to `quick-ecs` will be documented in this file.

### [1.7.20251008]
- Feature: Added `task-logs` action to stream logs for a specific running task
  - Shows each task's task definition version and how long it's been running
- Improvement: Clarified `logs` action to indicate service-wide streaming
- Docs: Updated README with both logging options
- Improvement: Exec sessions now forward Ctrl+C to the container instead of exiting
  - Added signal forwarding to child process so SIGINT/SIGTERM reach the remote shell
  - Prevents quick-ecs from terminating when sending interrupts during interactive exec
  - Normal keyboard input flows through to AWS CLI and session-manager-plugin without interference

### [1.7.20251007]
- Standardized terminal output using `github.com/bevelwork/quick_color` across commands
  - Consistent color palette and emphasis for statuses, headings, and tags
  - Improves readability in logs, checks, capacity and security groups views
- Minor documentation and metadata updates for release

### [1.6.20251002]
- Feature: enable `exec` action (guided enabling of ECS Exec on services)
- Feature: default actions and improved task definition field casing (camelCase)
- Improvement: logs auto-refresh behavior

### [1.5.20250926]
- Feature: initial Homebrew tap/release workflow
- Docs: README overhaul and usage examples
- Improvement: private mode to sanitize ARNs in output
- Various fixes to project structure and build configuration

### [1.4.20250925]
- Feature: "check" mode with many configuration and health validations
- Feature: ability to pull task definitions from a service
- Improvements and fixes from early usage and linting

—

Unreleased changes may be present on `main`. For installation and usage, see `README.md`.


