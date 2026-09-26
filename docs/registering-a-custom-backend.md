# Registering a custom DearMachine backend

Custom backend registration belongs to DearMachine, not to Machtiani. The
backend executable may use Machtiani, another coding agent, or any program that
implements DearMachine's stdin/close-path contract, but Machtiani itself does not
load or configure DearMachine backends.

Use the authoritative
[DearMachine custom-backend guide](https://github.com/tursomari/dearmachine/blob/main/docs/custom-backend-guide.md)
for the configuration schema, executable contract, health checks, wrapper
example, and troubleshooting steps. Keeping that contract in the DearMachine
repository prevents the two projects from publishing divergent instructions.

When testing a backend that invokes machtiani, ensure the intended machtiani
binary is on `PATH` and follow this repository's
[Machtiani runbook](./machtiani-runbook.md) for project initialization and
session handling.
