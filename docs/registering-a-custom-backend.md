# Registering a custom DearMachine backend

Custom backend registration belongs to DearMachine, not to mct-agent. The
backend executable may use mct, another coding agent, or any program that
implements DearMachine's stdin/close-path contract, but mct itself does not
load or configure DearMachine backends.

Use the authoritative
[DearMachine custom-backend guide](https://github.com/7db9a/DearMachine/blob/main/docs/custom-backend-guide.md)
for the configuration schema, executable contract, health checks, wrapper
example, and troubleshooting steps. Keeping that contract in the DearMachine
repository prevents the two projects from publishing divergent instructions.

When testing a backend that invokes machtiani, ensure the intended machtiani
binary is on `PATH` and follow this repository's
[mct-agent runbook](./mct-agent-runbook.md) for project initialization and
session handling.
