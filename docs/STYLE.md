# Documentation Style Guide

Conventions for writing and maintaining Markdown documentation in this repository.

## Heading Hierarchy

Use headings to create structure, not emphasis. Each document follows this hierarchy:

- `#` — Page title. One per document. Short noun phrase, not a sentence.
- `##` — Major sections. The primary table of contents level.
- `###` — Subsections. Use for numbered features or sub-topics under a `##`.
- `####` — Item-level entries. Use for individual items (patches, files, modules) within a subsection.

Do not skip levels (e.g. `#` followed by `###`).

## Item-Level Entries

When listing individual items within a section (files, patches, modules), use `####` headings with an em-dash separator:

```markdown
#### `net_dgrm.c.patch` — datagram layer

Description paragraph here.

#### `common.c.patch` — `COM_FileBase`

Description paragraph here.
```

The `####` level provides proper document structure while keeping entries visually distinct from the parent `###` subsection.

## Tables

Use tables for structured reference data: file listings, environment variables, platform support, patch overviews.

```markdown
| File | Purpose |
|------|---------|
| `sys_wasm.c` | System layer: main loop, file I/O, timing. |
| `vid_wasm.c` | Video and input: WebGL2, pointer lock. |
```

For sections with many items, lead with a summary table, then follow with detailed `####` entries below:

```markdown
## Overview

| Patch | Severity | Fix |
|-------|----------|-----|
| `foo.patch` | crash | Brief description |

## Details

#### `foo.patch` — short label

Full explanation.
```

## Prose

- **Concise and technical.** State what the code does, what the bug is, what the fix is. One paragraph per item when possible.
- **No marketing language.** Avoid superlatives, hype, or filler ("simply", "easily", "just").
- **Active voice.** "The fix replaces X with Y" not "X was replaced with Y by the fix."
- **End with the fix.** For bugfix/patch entries, close with "Fix: ..." so the resolution is always easy to find.

## Code Blocks

Always use fenced blocks with a language tag:

````markdown
```bash
make -f Makefile.dedicated
```

```c
snprintf(buf, sizeof(buf), "%s", value);
```
````

Inline code (`` ` ``) for identifiers, filenames, commands, and flags referenced in prose.

## Links

Use relative paths for cross-references within the repo:

```markdown
See [`ARCHITECTURE.md`](../docs/ARCHITECTURE.md) for details.
```

Do not use absolute GitHub URLs for files that live in the same tree.

## Formatting

- **No emojis.**
- **One blank line** between all block elements (headings, paragraphs, tables, code blocks, lists).
- **Ordered lists** (`1.`) for sequences and steps. **Unordered lists** (`-`) for non-sequential items.
- **Bold** for emphasis within prose. *Italic* sparingly, only for introducing a term.
