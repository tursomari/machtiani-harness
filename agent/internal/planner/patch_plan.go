package planner

// PatchPlanItem captures a single patch task in the session plan.
type PatchPlanItem struct {
	Description string `json:"description"`
	Complete    bool   `json:"complete"`
}

// PatchPlan aggregates patch tasks for a session.
type PatchPlan struct {
	Goal  string          `json:"goal"`
	Items []PatchPlanItem `json:"items"`
}

// Progress returns the total number of items and how many are complete.
func (p *PatchPlan) Progress() (total int, complete int) {
	if p == nil {
		return 0, 0
	}
	total = len(p.Items)
	for _, item := range p.Items {
		if item.Complete {
			complete++
		}
	}
	return total, complete
}

// AllComplete reports whether all plan items are marked complete.
// An empty or nil plan is treated as incomplete.
func (p *PatchPlan) AllComplete() bool {
	if p == nil || len(p.Items) == 0 {
		return false
	}
	for _, item := range p.Items {
		if !item.Complete {
			return false
		}
	}
	return true
}
