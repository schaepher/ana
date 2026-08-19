package cli

import (
	"fmt"
	"os"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"go.uber.org/zap"
)

// cmdPrecompute 全量 relations 预计算（Q228）：
//
//	codeintel precompute relations --repo <path>
//
// 前台同步执行：逐表计算全库表间关联，每批更新 relation_progress
// 进度（done_count/total_count，按 build_id 持久化），完成写
// relation_candidates 缓存 + status=done。之后 CLI query relations
// --all / serve /api/er 全量查询直接命中缓存（0.1s 级），不再现场
// 计算。serve 端对未计算仓库的首次全量请求也会自动后台计算兜底。
func cmdPrecompute(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdPrecompute")
	defer logger.Debug("exit cmdPrecompute")
	repoDir, rest, err := parseRepoFlag(args)
	if err != nil || repoDir == "" {
		fmt.Fprintln(os.Stderr, "--repo 是必需的")
		return 2
	}
	sub := "relations"
	for _, a := range rest {
		if a == "--json" {
			continue
		}
		sub = a
	}
	if sub != "relations" {
		fmt.Fprintf(os.Stderr, "unknown precompute target: %s（当前支持 relations）\n", sub)
		return 2
	}
	db, err := sqlite.Open(repoDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	r := sqlite.NewRepo(db)

	// 已完成直接提示
	p, err := r.RelationProgress()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if p.Status == "done" {
		fmt.Printf("全量 relations 已计算完成（%d 表）——查询直接命中缓存\n", p.Total)
		return 0
	}

	fmt.Printf("开始计算全量 relations……\n")
	last := -1
	err = r.PrecomputeAllRelations(func(done, total int) {
		// 每 10% 打印一行进度（小仓库瞬间完成时只打印最终行）
		step := total / 10
		if step < 1 {
			step = 1
		}
		if done-last >= step || done == total {
			last = done
			fmt.Printf("\r计算关联中 %d/%d 表", done, total)
			if done == total {
				fmt.Println()
			}
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	p, err = r.RelationProgress()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if p.Status != "done" {
		// 抢占失败（已有任务在跑）——提示等待
		fmt.Printf("计算任务已在运行中（进度 %d/%d）——可稍后重试查询或本命令查看进度\n", p.Done, p.Total)
		return 0
	}
	// 结果摘要
	rels, err := r.GetAllTableRelations("")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("完成：%d 表 · %d 条关联（fk %d / 键 %d / 写 %d / 读 %d）\n",
		p.Total, len(rels), countByType(rels, "fk"), countByType(rels, "query"),
		countByType(rels, "write"), countByType(rels, "read"))
	return 0
}

// countByType 统计指定类型的关联数。
func countByType(rels []*domain.TableRelation, typ string) int {
	n := 0
	for _, r := range rels {
		if r.Type == typ {
			n++
		}
	}
	return n
}
