package cli

import (
	"fmt"

	"github.com/Alisjj/durablemux-verifier/internal/checks"
	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/review"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
	"github.com/Alisjj/durablemux-verifier/internal/store"
)

func runStageChecks(stage *model.Stage, ctx *runner.Context) ([]model.CheckResult, error) {
	var results []model.CheckResult
	for _, group := range stage.Checks {
		rs, err := checks.RunBuiltin(group, ctx)
		if err != nil {
			return nil, err
		}
		results = append(results, rs...)
	}
	return append(results, checks.RunCustomChecks(stage.Number, ctx)...), nil
}

// Previous passes are history, not a regression test of the current binary.
func verifyPrerequisites(root string, cfg map[string]any, stages map[int]*model.Stage, s *store.Store, number int, sha string) error {
	for _, n := range sortedKeys(stages) {
		if n >= number {
			break
		}
		stage := stages[n]
		if stage.Mode != "auto" {
			if err := review.Complete(stage, s, root, sha); err != nil || !s.Approved(n) || review.EvidenceCount(s, n, root) < stage.MinimumEvidence {
				return fmt.Errorf("stage %d needs a complete approved review for this binary; run dmux-verify review %d", n, n)
			}
		}
		if len(stage.Checks) == 0 && !hasCustomChecks(cfg, n) {
			continue
		}
		fmt.Printf("Regression check: stage %d\n", n)
		ctx, err := runner.New(root, cfg)
		if err != nil {
			return err
		}
		results, err := runStageChecks(stage, ctx)
		ctx.Close()
		if err != nil {
			return err
		}
		passed := len(results) > 0
		var records []any
		for _, r := range results {
			passed = passed && r.Passed
			records = append(records, map[string]any{"name": r.Name, "passed": r.Passed, "detail": r.Detail, "stdout": r.Stdout, "stderr": r.Stderr, "duration_seconds": r.DurationSeconds})
			if !r.Passed {
				fmt.Printf("FAIL  %s — %s\n", r.Name, r.Detail)
			}
		}
		payload := map[string]any{"at": store.UTCNow(), "passed": passed, "stage": n, "mode": stage.Mode, "checks": records, "binary_sha256": sha, "git_commit": gitOrNil(root), "evidence_count": s.EvidenceCount(n), "manual_approved": s.Approved(n), "regression_for_stage": number}
		if err := s.RecordRun(n, payload); err != nil {
			return err
		}
		if !passed {
			return fmt.Errorf("stage %d regressed; fix it before verifying stage %d", n, number)
		}
	}
	return nil
}

func hasCustomChecks(cfg map[string]any, stage int) bool {
	cc, _ := cfg["custom_checks"].(map[string]any)
	items, _ := cc[fmt.Sprint(stage)].([]any)
	return len(items) > 0
}
