# Live Treatment Monitoring

*These instructions are likely transient until automated heartbeat and alerting is built.*

## Finding the Right Container

There are often multiple Docker containers running concurrently. The naming convention from run-single-treatment.sh is task-name double-underscore random-suffix dash main dash 1. Use docker ps to list them. Do not assume only one.

## Finding the Active Session ID

Two methods: from the process table (ps aux inside container, look for mct-agent with --session-id flag) and from the filesystem listing .machtiani/sessions. Note there may be both a meta-orchestrator session and an agent session. The meta-orchestrator creates its own meta-session which may be done while the agent session is still running. Do not confuse them.

## The Three Health Indicators

| Check | What it tells you | Caveat |
|-------|-------------------|--------|
| ps aux | process is alive | does not tell you if it is making progress |
| conversation.json last modified | agent is writing LLM thoughts | only updates when a full assistant turn completes |
| child shell-agent trajectory | individual commands are executing | this is the real heartbeat |

## The Critical Gotcha About agent.jsonl

The parent agent.jsonl trajectory only records events when mct-agent produces a final answer for the meta-orchestrator. It is not a real-time log. Silence there does not mean a dead agent. Always check the child shell-agent trajectory first. This was the biggest lesson learned in a real session.

## Shell-Agent Activity, the Real Heartbeat

The shell-agent creates a child session directory under shell-agent/number inside the parent session. The trajectory file trajectory.json records each observe-command-answer step.

List child directories:

```bash
ls .machtiani/sessions/<parent-session-id>/shell-agent/
```

Tail the last few lines of trajectory.json:

```bash
tail -20 .machtiani/sessions/<parent-session-id>/shell-agent/<child-id>/trajectory.json
```

Count answers with grep:

```bash
grep -c '"type":"answer"' .machtiani/sessions/<parent-session-id>/shell-agent/<child-id>/trajectory.json
```

If this file is updating within the last minute, the agent is actively working.

## Phase Model

- **Initial implementation** - agent reads, plans, makes code changes, shell commands include git branch, git checkout, file edits, go build.
- **Test failure fix loop** - agent runs tests, gets failures, iterates.
- **Waiting on meta-orchestrator** - mct-agent produced a final answer and exited, orchestrator classifies it, child shell-agent trajectory goes silent.
- **Re-invoked on CONTINUE** - orchestrator launches fresh mct-agent with --session-id and -t flag, new child shell-agent session appears.

## Evaluating Scope Proportionality

You cannot judge whether the agent is rabbit-holing without reading the original task instruction.

Read the instruction inside the container:

```bash
cat /app/instruction.md
```

List changed files:

```bash
git diff master...branch --name-only
```

In a real session we initially thought the agent was rabbit-holing because we assumed a small fix, but the instruction actually required a large feature. Never assume scope without reading the instruction.

## Quick Monitoring One-Liner

```bash
CONTAINER=my-container && SESSION=$(docker exec $CONTAINER ls .machtiani/sessions/ | tail -1) && \
echo "Branch: $(docker exec $CONTAINER git branch --show-current)" && \
echo "Child last step: $(docker exec $CONTAINER bash -c "ls -t .machtiani/sessions/$SESSION/shell-agent/ 2>/dev/null | head -1")" && \
echo "Process CPU: $(docker exec $CONTAINER ps aux | grep mct-agent | awk '{print \$3}')" && \
echo "conversation.json mod: $(docker exec $CONTAINER stat -c %y .machtiani/sessions/$SESSION/conversation.json 2>/dev/null)"
```

## Common Pitfalls

| Gotcha | What I learned |
|--------|----------------|
| Parent vs child trajectory | The parent agent.jsonl only updates on final answers; use child trajectory.json for real-time monitoring |
| Meta-session vs agent session | The meta-orchestrator has its own session that may finish before the agent session |
| 56-minute stall was actually looking at wrong file | We were watching agent.jsonl while the child shell-agent was actively running |
| Rabbit hole misjudgment | Without reading the original instruction, we assumed a small fix when the task actually required a large feature |
| No child PID for shell-agent is normal | The shell-agent does not fork; do not expect a separate process |
| git diff shows nothing if changes are committed | Use diff against master to see the full scope of changes |
| conversation.json timestamp lags behind shell activity | conversation.json updates only on assistant turn boundaries |

This document captures manual monitoring procedures derived from operational experience. As the system matures, these checks should be replaced or supplemented by automated heartbeat monitoring and alerting on the shell-agent trajectory stream.
