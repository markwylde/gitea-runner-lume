# AGENTS — product specifications

`CORE.md` is the product-level source of truth. `features/` contains one
canonical specification per capability.

- Use `tasks/` only for active, unimplemented work. Task files have a numeric
  prefix, goal, governing features, dependencies, implementation checklist,
  acceptance checks, and definition of done.
- Move completed task files to `tasks_completed/` without rewriting their
  checked history.
- Feature specifications use present tense and contain no implementation
  status, transition plan, or stale checklist.
- Resolve conflicts in this order: security boundaries, `CORE.md`, governing
  feature specification, then active task plan.

