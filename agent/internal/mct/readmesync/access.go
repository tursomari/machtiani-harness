package readmesync

import readmemgr "github.com/tursomari/machtiani/agent/internal/mct/internal/readme"

// READMECommitForProject resolves the internal README commit matching the provided project commit.
func READMECommitForProject(projectCommit string) (string, error) {
	return readmemgr.GetREADMECommitForProject(projectCommit)
}

// CheckoutReadonlyREADME materializes the internal README for the given project commit into the workspace.
func CheckoutReadonlyREADME(projectCommit string) error {
	return readmemgr.CheckoutReadonlyReadme(projectCommit)
}
