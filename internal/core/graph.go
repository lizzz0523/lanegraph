package core

import "slices"

type GraphRow struct {
	NodeCol int
	Top     [][2]int
	Bottom  [][2]int
}

type Graph struct {
	Rows      []GraphRow
	LaneCount int
}

const maxLanes = 24

func ComputeGraph(commits []Commit) Graph {
	rows := make([]GraphRow, 0, len(commits))
	lanes := []string{}
	laneCount := 1

	for _, commit := range commits {
		expected := []int{}
		for i := range lanes {
			if lanes[i] == commit.Hash {
				expected = append(expected, i)
			}
		}

		var nodeCol int
		if len(expected) > 0 {
			nodeCol = expected[0]
		} else {
			nodeCol = freeSlot(lanes)
		}

		top := [][2]int{}
		for i := range lanes {
			if lanes[i] == "" {
				continue
			}
			if slices.Contains(expected, i) {
				top = append(top, [2]int{i, nodeCol})
			} else {
				top = append(top, [2]int{i, i})
			}
		}

		for _, i := range expected {
			lanes[i] = ""
		}
		if nodeCol < len(lanes) {
			lanes[nodeCol] = ""
		}

		parentCols := []int{}
		for i, parent := range commit.Parents {
			// 第一个 parent 始终沿用节点所在 lane，保持主线连续；
			// 若该 parent 已在其他 lane 中，会在它自己那一行汇合
			parentCol := nodeCol
			if i > 0 {
				parentCol = slices.IndexFunc(lanes, func(lane string) bool { return lane == parent })
				if parentCol == -1 {
					parentCol = freeSlot(lanes)
				}
			}
			lanes = ensureLen(lanes, parentCol)
			lanes[parentCol] = parent
			if !slices.Contains(parentCols, parentCol) {
				parentCols = append(parentCols, parentCol)
			}
		}

		bottom := [][2]int{}
		for i := range lanes {
			if lanes[i] == "" {
				continue
			}
			if slices.Contains(parentCols, i) {
				bottom = append(bottom, [2]int{nodeCol, i})
			} else {
				bottom = append(bottom, [2]int{i, i})
			}
		}

		for len(lanes) > 0 && lanes[len(lanes)-1] == "" {
			lanes = lanes[:len(lanes)-1]
		}

		for _, edge := range slices.Concat(top, bottom) {
			laneCount = max(laneCount, edge[0]+1, edge[1]+1)
		}
		laneCount = max(laneCount, nodeCol+1, len(lanes))

		rows = append(rows, GraphRow{NodeCol: nodeCol, Top: top, Bottom: bottom})
	}

	return Graph{Rows: rows, LaneCount: min(maxLanes, max(1, laneCount))}
}

func freeSlot(lanes []string) int {
	slot := slices.IndexFunc(lanes, func(lane string) bool { return lane == "" })
	if slot != -1 {
		return slot
	}
	return len(lanes)
}

func ensureLen(lanes []string, idx int) []string {
	for idx >= len(lanes) {
		lanes = append(lanes, "")
	}
	return lanes
}
