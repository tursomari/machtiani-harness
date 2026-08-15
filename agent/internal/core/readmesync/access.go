package readmesync

import readmemgr "github.com/tursomari/machtiani/agent/internal/core/readme"

// READMECommitForProject resolves the internal README commit matching the provided project commit.
func READMECommitForProject(projectCommit string) (string, error) {
	return readmemgr.GetREADMECommitForProject(projectCommit)
}

// ReadREADMEForProject reads the immutable internal README object associated
// with projectCommit. It does not depend on or mutate the shared README
// worktree.
func ReadREADMEForProject(projectCommit string) (string, string, error) {
	return readmemgr.ReadREADMEForProject(projectCommit)
}

// ReadREADMEForProjectAt reads the immutable internal README associated with
// projectCommit from the project store resolved from projectRoot.
func ReadREADMEForProjectAt(projectRoot, projectCommit string) (string, string, error) {
	return readmemgr.ReadREADMEForProjectAt(projectRoot, projectCommit)
}

// CheckoutReadonlyREADME materializes the internal README for the given project commit into the workspace.
func CheckoutReadonlyREADME(projectCommit string) error {
	return readmemgr.CheckoutReadonlyReadme(projectCommit)
}
