package main

import (
	"fmt"
	"os"

	"github.com/aswe/aswe/internal/state"
	"github.com/spf13/cobra"
)

// newStatusCmd 构造 "aswe status <change-id>" 子命令.
func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <change-id>",
		Short: "查看某个 change 的执行进度 (stages + modules + units)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(args[0])
		},
	}
	return cmd
}

func runStatus(changeID string) error {
	cwd, _ := os.Getwd()
	_, workspace, _, err := loadConfigAndWorkspace(gflags.cfgPath, cwd)
	if err != nil {
		return err
	}
	store, err := state.Open(workspace, changeID)
	if err != nil {
		return err
	}
	st := store.State()

	fmt.Printf("change-id      : %s\n", st.ChangeID)
	fmt.Printf("workspace      : %s\n", st.WorkspaceDir)
	fmt.Printf("project dir    : %s\n", st.ProjectDir)
	fmt.Printf("current stage  : %s\n", st.CurrentStage)
	fmt.Printf("plan iteration : %d\n", st.PlanIteration)
	fmt.Printf("code iteration : %d\n", st.CodeIteration)
	fmt.Printf("updated at     : %s\n", st.UpdatedAt.Format("2006-01-02 15:04:05"))
	fmt.Println("\nNodes:")
	for _, stage := range []state.Stage{
		state.StageSpec, state.StagePlan, state.StagePlanReview,
		state.StageDev, state.StageReview, state.StageTest,
	} {
		n, ok := st.Nodes[stage]
		if !ok {
			fmt.Printf("  %-12s  <未执行>\n", stage)
			continue
		}
		fmt.Printf("  %-12s  status=%s adapter=%s\n", stage, n.Status, n.Adapter)
		if n.Error != "" {
			fmt.Printf("                error=%s\n", n.Error)
		}
	}

	if len(st.Modules) > 0 {
		fmt.Printf("\nModules (max-unit-loops=%d):\n", st.MaxUnitLoops)
		for _, m := range st.Modules {
			done := 0
			for _, u := range m.Units {
				if u.Status == state.UnitDone {
					done++
				}
			}
			fmt.Printf("  📦 %s [%s]  %s  (%d/%d units done)\n",
				m.ID, m.Status, m.Title, done, len(m.Units))
			for _, u := range m.Units {
				fmt.Printf("     - %-8s iter=%d  status=%-15s  %s\n",
					u.ID, u.Iteration, u.Status, u.Title)
				if u.LastError != "" {
					fmt.Printf("       error: %s\n", u.LastError)
				}
			}
		}
	}
	return nil
}
