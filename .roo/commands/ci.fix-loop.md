---
description: Work untill all CI checks for current branch PR pass
---

## Goal

Make sure the PR is ready to be merged - all checks pass, code quality is high.

## Workflow steps

Work with GitHub CI in loop as long as there are build failures for current branch/PR.

1. Start a new task in code mode that will execute `./scripts/fetch-ci-logs.sh` to get the build results (the script will wait if the results are not ready).
2. For each failure start a new task. This task should start in "ask" mode in order to analyze failure report, then (the same task!) should switch to "architect" mode to plan actions, and finally it should switch to "code" mode to make chagnes and commit+push changes.

Repeat until there is no issues left.

## Additional information

### Conventional Commits

Follow Conventional Commits rules when writing commit messages. The message should describe reason, goal and neccesary context for the change, not changes in the commit.

### Documentation

Do not write documentation, failure analysis, or work reports to files - these are not needed. Use commit messages to provide history of changes.

### Making decisions

Some issues are simple to diagnose and solve - do that without asking for feedback.

In some cases there problem is bigger, multi-layered, and solutions are not clear or come at a cost. In cases like this:
1. Provide a detailed analysis of the problem.
2. Describe solutions ideas. Analyze pros, cons, blast radius and future impact.
3. Ask for feedback using ask_followup_questions tool.

