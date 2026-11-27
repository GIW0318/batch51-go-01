// Package task keeps the navigation grid of a level together with the crowd of
// units walking it. Dynamic obstacles block cells, units claim the cells they
// stand on and only the paths that cross a changed region are recomputed.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Audit actions appended by the guard.
const (
	AuditAddObstacle    = "add_obstacle"
	AuditRemoveObstacle = "remove_obstacle"
	AuditPath           = "find_path"
	AuditClaim          = "claim_cell"
	AuditRelease        = "release_cell"
	AuditRecompute      = "recompute"
)

// Errors returned by the guard.
var (
	ErrBadGrid         = errors.New("navigation grid is invalid")
	ErrBadConfig       = errors.New("crowd configuration is invalid")
	ErrOutOfRange      = errors.New("cell is outside the navigation grid")
	ErrBadObstacle     = errors.New("obstacle is invalid")
	ErrObstacleExists  = errors.New("obstacle id already exists")
	ErrUnknownObstacle = errors.New("obstacle not found")
	ErrObstacleLimit   = errors.New("obstacle limit reached")
	ErrBadUnit         = errors.New("unit id is required")
	ErrBudgetExhausted = errors.New("path expansion budget exhausted")
	ErrBadRegion       = errors.New("region is invalid")
	ErrCellBlocked     = errors.New("cell is blocked")
	ErrNotOwner        = errors.New("unit does not own the cell")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Cell is one square of the navigation grid.
type Cell struct {
	X int
	Y int
}

// Region is an inclusive rectangle of cells.
type Region struct {
	MinX int
	MinY int
	MaxX int
	MaxY int
}

// Grid is the static level walkmesh. Blocked is indexed by y*Width+x; an empty
// slice means every cell is walkable.
type Grid struct {
	Width   int
	Height  int
	Blocked []bool
}

// Obstacle is a dynamic blocker such as a summoned pillar or a closed gate.
type Obstacle struct {
	ID     string
	Region Region
}

// CrowdConfig bounds the crowd bookkeeping of the guard.
type CrowdConfig struct {
	MaxUnits        int
	DensityLimit    int
	RecomputeBudget int
	MaxObstacles    int
	MaxAudit        int
}

// PathRequest asks for a route for one unit of the crowd.
type PathRequest struct {
	UnitID   string
	From     Cell
	To       Cell
	Priority int
}

// PathResult is the route returned to the unit controller.
type PathResult struct {
	UnitID  string
	Points  []Cell
	Cost    int
	Reached bool
	Partial bool
	Nearest Cell
}

// ClaimResult reports the outcome of one cell claim.
type ClaimResult struct {
	Cell    Cell
	Owner   string
	Granted bool
}

// RecomputeReport lists the units whose route was rebuilt.
type RecomputeReport struct {
	Units      []string
	Recomputed int
}

// AuditEntry is one recorded guard action.
type AuditEntry struct {
	Seq    int64
	Action string
	UnitID string
	Detail string
}

// GuardStats counts the work done by the guard.
type GuardStats struct {
	Queries    int
	Paths      int
	Recomputes int
	Claims     int
	Releases   int
	Degraded   int
}

type cachedPath struct {
	Request PathRequest
	Result  PathResult
}

type bfsNode struct {
	parent    Cell
	hasParent bool
	distance  int
}

// NavmeshCrowdGuard owns the grid, the dynamic obstacles, the cell claims and
// the cached routes of the crowd.
type NavmeshCrowdGuard struct {
	mu sync.Mutex

	grid           Grid
	cfg            CrowdConfig
	obstacles      map[string]Obstacle
	dynamicBlocked map[Cell]int
	claims         map[Cell]map[string]int
	paths          map[string]cachedPath
	stats          GuardStats
	audit          []AuditEntry
	auditSeq       int64
	degraded       bool
}

// NewNavmeshCrowdGuard validates the grid and the configuration.
func NewNavmeshCrowdGuard(grid Grid, cfg CrowdConfig) (*NavmeshCrowdGuard, error) {
	if !validGrid(grid) {
		return nil, ErrBadGrid
	}
	if !validConfig(cfg) {
		return nil, ErrBadConfig
	}
	g := &NavmeshCrowdGuard{
		grid:           copyGrid(grid),
		cfg:            cfg,
		obstacles:      make(map[string]Obstacle),
		dynamicBlocked: make(map[Cell]int),
		claims:         make(map[Cell]map[string]int),
		paths:          make(map[string]cachedPath),
	}
	return g, nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func validGrid(grid Grid) bool {
	if grid.Width <= 0 || grid.Height <= 0 {
		return false
	}
	if grid.Width > maxInt()/grid.Height {
		return false
	}
	size := grid.Width * grid.Height
	if len(grid.Blocked) != 0 && len(grid.Blocked) != size {
		return false
	}
	return true
}

func validConfig(cfg CrowdConfig) bool {
	return cfg.MaxUnits > 0 &&
		cfg.DensityLimit > 0 &&
		cfg.RecomputeBudget > 0 &&
		cfg.MaxObstacles > 0 &&
		cfg.MaxAudit > 0
}

func copyGrid(grid Grid) Grid {
	out := grid
	if len(grid.Blocked) != 0 {
		out.Blocked = append([]bool(nil), grid.Blocked...)
	}
	return out
}

func inGrid(grid Grid, c Cell) bool {
	return c.X >= 0 && c.Y >= 0 && c.X < grid.Width && c.Y < grid.Height
}

func staticBlocked(grid Grid, c Cell) bool {
	if len(grid.Blocked) == 0 {
		return false
	}
	return grid.Blocked[c.Y*grid.Width+c.X]
}

func validRegionShape(r Region) bool {
	return r.MinX <= r.MaxX && r.MinY <= r.MaxY
}

func regionInGrid(grid Grid, r Region) bool {
	return r.MinX >= 0 && r.MinY >= 0 && r.MaxX < grid.Width && r.MaxY < grid.Height
}

func regionContains(r Region, c Cell) bool {
	return c.X >= r.MinX && c.X <= r.MaxX && c.Y >= r.MinY && c.Y <= r.MaxY
}

func cloneResult(in PathResult) PathResult {
	out := in
	out.Points = append([]Cell(nil), in.Points...)
	return out
}

func cellDetail(c Cell) string {
	return fmt.Sprintf("%d,%d", c.X, c.Y)
}

func regionDetail(r Region) string {
	return fmt.Sprintf("%d,%d-%d,%d", r.MinX, r.MinY, r.MaxX, r.MaxY)
}

func (g *NavmeshCrowdGuard) inRange(c Cell) bool {
	return inGrid(g.grid, c)
}

func (g *NavmeshCrowdGuard) blockedLocked(c Cell) bool {
	if staticBlocked(g.grid, c) {
		return true
	}
	return g.dynamicBlocked[c] > 0
}

func (g *NavmeshCrowdGuard) applyDynamicRegionLocked(r Region, delta int) {
	for y := r.MinY; y <= r.MaxY; y++ {
		for x := r.MinX; x <= r.MaxX; x++ {
			c := Cell{X: x, Y: y}
			n := g.dynamicBlocked[c] + delta
			if n <= 0 {
				delete(g.dynamicBlocked, c)
			} else {
				g.dynamicBlocked[c] = n
			}
		}
	}
}

func (g *NavmeshCrowdGuard) appendAuditLocked(action, unitID, detail string) {
	g.auditSeq++
	g.audit = append(g.audit, AuditEntry{Seq: g.auditSeq, Action: action, UnitID: unitID, Detail: detail})
	if len(g.audit) > g.cfg.MaxAudit {
		drop := len(g.audit) - g.cfg.MaxAudit
		g.audit = append([]AuditEntry(nil), g.audit[drop:]...)
	}
}

func (g *NavmeshCrowdGuard) obstacleListLocked() []Obstacle {
	out := make([]Obstacle, 0, len(g.obstacles))
	for _, obs := range g.obstacles {
		out = append(out, obs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AddObstacle adds a dynamic blocker and marks its cells.
func (g *NavmeshCrowdGuard) AddObstacle(obs Obstacle) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.TrimSpace(obs.ID) == "" {
		return ErrBadObstacle
	}
	if !validRegionShape(obs.Region) {
		return ErrBadObstacle
	}
	if !regionInGrid(g.grid, obs.Region) {
		return ErrOutOfRange
	}
	if _, ok := g.obstacles[obs.ID]; ok {
		return ErrObstacleExists
	}
	if len(g.obstacles) >= g.cfg.MaxObstacles {
		return ErrObstacleLimit
	}
	g.obstacles[obs.ID] = obs
	g.applyDynamicRegionLocked(obs.Region, 1)
	g.appendAuditLocked(AuditAddObstacle, "", obs.ID)
	return nil
}

// RemoveObstacle removes a dynamic blocker by id.
func (g *NavmeshCrowdGuard) RemoveObstacle(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	obs, ok := g.obstacles[id]
	if !ok {
		return ErrUnknownObstacle
	}
	delete(g.obstacles, id)
	g.applyDynamicRegionLocked(obs.Region, -1)
	g.appendAuditLocked(AuditRemoveObstacle, "", obs.ID)
	return nil
}

// Obstacles lists the active dynamic blockers ordered by id.
func (g *NavmeshCrowdGuard) Obstacles() []Obstacle {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.obstacleListLocked()
}

// FindPath computes a four way route around static and dynamic blockers.
func (g *NavmeshCrowdGuard) FindPath(req PathRequest) (PathResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.TrimSpace(req.UnitID) == "" {
		return PathResult{}, ErrBadUnit
	}
	if !g.inRange(req.From) || !g.inRange(req.To) {
		return PathResult{}, ErrOutOfRange
	}
	g.stats.Queries++
	if g.blockedLocked(req.From) {
		return PathResult{}, ErrCellBlocked
	}
	res, err := g.computePath(req.From, req.To, g.cfg.RecomputeBudget)
	if err != nil {
		return PathResult{}, err
	}
	res.UnitID = req.UnitID
	if res.Reached {
		g.stats.Paths++
	}
	g.paths[req.UnitID] = cachedPath{Request: req, Result: cloneResult(res)}
	g.appendAuditLocked(AuditPath, req.UnitID, cellDetail(req.From)+"->"+cellDetail(req.To))
	return cloneResult(res), nil
}

// ClaimCell records that a unit stands on a cell.
func (g *NavmeshCrowdGuard) ClaimCell(cell Cell, unitID string, priority int) (ClaimResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.TrimSpace(unitID) == "" {
		return ClaimResult{}, ErrBadUnit
	}
	if !g.inRange(cell) {
		return ClaimResult{}, ErrOutOfRange
	}
	if g.blockedLocked(cell) {
		return ClaimResult{}, ErrCellBlocked
	}

	occupants := g.claims[cell]
	if existingPriority, ok := occupants[unitID]; ok {
		if priority > existingPriority {
			occupants[unitID] = priority
			g.evictLowerLocked(cell, occupants, unitID, priority)
		}
		return ClaimResult{Cell: cell, Owner: unitID, Granted: true}, nil
	}

	if len(occupants) != 0 {
		highest := -maxInt() - 1
		for _, p := range occupants {
			if p > highest {
				highest = p
			}
		}
		if priority < highest {
			return ClaimResult{Cell: cell, Owner: dominantOwner(occupants, highest), Granted: false}, nil
		}
	}

	if occupants == nil {
		occupants = make(map[string]int)
		g.claims[cell] = occupants
	}
	occupants[unitID] = priority
	g.evictLowerLocked(cell, occupants, unitID, priority)
	g.stats.Claims++
	g.appendAuditLocked(AuditClaim, unitID, cellDetail(cell))
	return ClaimResult{Cell: cell, Owner: unitID, Granted: true}, nil
}

func dominantOwner(occupants map[string]int, priority int) string {
	owner := ""
	for unit, p := range occupants {
		if p != priority {
			continue
		}
		if owner == "" || unit < owner {
			owner = unit
		}
	}
	return owner
}

func (g *NavmeshCrowdGuard) evictLowerLocked(cell Cell, occupants map[string]int, except string, priority int) {
	for unit, p := range occupants {
		if unit == except || p >= priority {
			continue
		}
		delete(occupants, unit)
		if cached, ok := g.paths[unit]; ok {
			if _, err := g.recomputePathLocked(unit, cached); err != nil {
				delete(g.paths, unit)
			}
		}
	}
	if len(occupants) == 0 {
		delete(g.claims, cell)
	}
}

func (g *NavmeshCrowdGuard) recomputePathLocked(unit string, cached cachedPath) (PathResult, error) {
	res, err := g.computePath(cached.Request.From, cached.Request.To, g.cfg.RecomputeBudget)
	if err != nil {
		return PathResult{}, err
	}
	res.UnitID = unit
	cached.Result = cloneResult(res)
	g.paths[unit] = cached
	g.stats.Recomputes++
	return res, nil
}

// ReleaseCell removes one unit from a cell it owns.
func (g *NavmeshCrowdGuard) ReleaseCell(cell Cell, unitID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.TrimSpace(unitID) == "" {
		return ErrBadUnit
	}
	if !g.inRange(cell) {
		return ErrOutOfRange
	}
	occupants, ok := g.claims[cell]
	if !ok {
		return ErrNotOwner
	}
	if _, ok := occupants[unitID]; !ok {
		return ErrNotOwner
	}
	delete(occupants, unitID)
	if len(occupants) == 0 {
		delete(g.claims, cell)
	}
	g.stats.Releases++
	g.appendAuditLocked(AuditRelease, unitID, cellDetail(cell))
	return nil
}

// CrowdAt reports how many units stand on a cell.
func (g *NavmeshCrowdGuard) CrowdAt(cell Cell) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.inRange(cell) {
		return 0
	}
	return len(g.claims[cell])
}

// AvoidStep returns the step a unit may take, or its current cell when the
// target cell is too crowded.
func (g *NavmeshCrowdGuard) AvoidStep(unitID string, from, desired Cell) (Cell, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.TrimSpace(unitID) == "" {
		return Cell{}, ErrBadUnit
	}
	if !g.inRange(from) || !g.inRange(desired) {
		return Cell{}, ErrOutOfRange
	}
	if desired == from {
		return from, nil
	}
	if g.blockedLocked(desired) {
		return Cell{}, ErrCellBlocked
	}
	if len(g.claims[desired]) >= g.cfg.DensityLimit {
		g.degraded = true
		g.stats.Degraded++
		return from, nil
	}
	g.degraded = false
	return desired, nil
}

// Recompute rebuilds the routes that cross the given region.
func (g *NavmeshCrowdGuard) Recompute(region Region) (RecomputeReport, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !validRegionShape(region) {
		return RecomputeReport{}, ErrBadRegion
	}
	if !regionInGrid(g.grid, region) {
		return RecomputeReport{}, ErrOutOfRange
	}

	affected := make([]string, 0)
	for unit, cached := range g.paths {
		if pathCrossesRegion(cached.Result.Points, region) {
			affected = append(affected, unit)
		}
	}
	sort.Strings(affected)

	updates := make(map[string]cachedPath, len(affected))
	for _, unit := range affected {
		cached := g.paths[unit]
		res, err := g.computePath(cached.Request.From, cached.Request.To, g.cfg.RecomputeBudget)
		if err != nil {
			return RecomputeReport{}, err
		}
		res.UnitID = unit
		updates[unit] = cachedPath{Request: cached.Request, Result: cloneResult(res)}
	}

	report := RecomputeReport{Units: affected, Recomputed: len(updates)}
	for unit, cached := range updates {
		g.paths[unit] = cached
		g.stats.Recomputes++
	}
	g.appendAuditLocked(AuditRecompute, "", regionDetail(region))
	return report, nil
}

func pathCrossesRegion(points []Cell, region Region) bool {
	for _, c := range points {
		if regionContains(region, c) {
			return true
		}
	}
	return false
}

// NearestReachable returns the closest walkable cell reachable from from.
func (g *NavmeshCrowdGuard) NearestReachable(from Cell) (Cell, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.inRange(from) {
		return Cell{}, false, ErrOutOfRange
	}
	if !g.blockedLocked(from) {
		return from, true, nil
	}

	best := Cell{}
	found := false
	bestDistance := maxInt()
	for y := 0; y < g.grid.Height; y++ {
		for x := 0; x < g.grid.Width; x++ {
			c := Cell{X: x, Y: y}
			if g.blockedLocked(c) {
				continue
			}
			d := manhattan(c, from)
			if !found || d < bestDistance {
				best = c
				bestDistance = d
				found = true
			}
		}
	}
	return best, found, nil
}

// SetDensityLimit updates the crowd threshold used by AvoidStep.
func (g *NavmeshCrowdGuard) SetDensityLimit(limit int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if limit <= 0 {
		return ErrBadConfig
	}
	g.cfg.DensityLimit = limit
	return nil
}

// Degraded reports whether the last avoidance decision was degraded.
func (g *NavmeshCrowdGuard) Degraded() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.degraded
}

// Stats reports the counters of the guard.
func (g *NavmeshCrowdGuard) Stats() GuardStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// Audit returns the recorded actions, oldest first.
func (g *NavmeshCrowdGuard) Audit() []AuditEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]AuditEntry(nil), g.audit...)
}

// computePath runs a breadth first search over four way neighbours. Every
// popped cell consumes one unit of the expansion budget.
func (g *NavmeshCrowdGuard) computePath(from, to Cell, budget int) (PathResult, error) {
	if from == to {
		return PathResult{Points: []Cell{from}, Cost: 0, Reached: true, Nearest: to}, nil
	}
	if g.blockedLocked(from) {
		return PathResult{}, ErrCellBlocked
	}

	neighbours := [4]Cell{{X: 0, Y: 1}, {X: 1, Y: 0}, {X: 0, Y: -1}, {X: -1, Y: 0}}
	visited := make(map[Cell]bfsNode)
	visited[from] = bfsNode{distance: 0}
	queue := []Cell{from}
	head := 0
	expanded := 0
	found := false

	nearest := from
	nearestDistance := manhattan(from, to)

	for head < len(queue) {
		if expanded >= budget {
			return PathResult{}, ErrBudgetExhausted
		}
		current := queue[head]
		head++
		expanded++
		if current == to {
			found = true
			break
		}
		for _, delta := range neighbours {
			next := Cell{X: current.X + delta.X, Y: current.Y + delta.Y}
			if !g.inRange(next) || g.blockedLocked(next) {
				continue
			}
			if _, ok := visited[next]; ok {
				continue
			}
			visited[next] = bfsNode{parent: current, hasParent: true, distance: visited[current].distance + 1}
			queue = append(queue, next)
			d := manhattan(next, to)
			if d < nearestDistance {
				nearest = next
				nearestDistance = d
			}
		}
	}

	target := to
	if !found {
		target = nearest
	}
	points := reconstructPath(visited, target)
	if !found {
		return PathResult{
			Points:  points,
			Cost:    len(points) - 1,
			Reached: false,
			Partial: true,
			Nearest: nearest,
		}, nil
	}
	return PathResult{
		Points:  points,
		Cost:    len(points) - 1,
		Reached: true,
		Partial: false,
		Nearest: to,
	}, nil
}

func reconstructPath(visited map[Cell]bfsNode, target Cell) []Cell {
	points := make([]Cell, 0)
	for {
		points = append(points, target)
		node := visited[target]
		if !node.hasParent {
			break
		}
		target = node.parent
	}
	for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
		points[i], points[j] = points[j], points[i]
	}
	return points
}

func manhattan(a, b Cell) int {
	dx := a.X - b.X
	if dx < 0 {
		dx = -dx
	}
	dy := a.Y - b.Y
	if dy < 0 {
		dy = -dy
	}
	return dx + dy
}

const snapshotVersion = 1

type snapshotClaim struct {
	Cell     Cell   `json:"cell"`
	UnitID   string `json:"unit_id"`
	Priority int    `json:"priority"`
}

type snapshotPath struct {
	Request PathRequest `json:"request"`
	Result  PathResult  `json:"result"`
}

type guardSnapshot struct {
	Version   int             `json:"version"`
	Grid      Grid            `json:"grid"`
	Config    CrowdConfig     `json:"config"`
	Degraded  bool            `json:"degraded"`
	Obstacles []Obstacle      `json:"obstacles"`
	Claims    []snapshotClaim `json:"claims"`
	Paths     []snapshotPath  `json:"paths"`
	Stats     GuardStats      `json:"stats"`
	Audit     []AuditEntry    `json:"audit"`
	AuditSeq  int64           `json:"audit_seq"`
}

// Snapshot serialises the grid state, the claims and the cached routes.
func (g *NavmeshCrowdGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	snap := guardSnapshot{
		Version:   snapshotVersion,
		Grid:      copyGrid(g.grid),
		Config:    g.cfg,
		Degraded:  g.degraded,
		Obstacles: g.obstacleListLocked(),
		Stats:     g.stats,
		Audit:     append([]AuditEntry(nil), g.audit...),
		AuditSeq:  g.auditSeq,
	}

	cells := make([]Cell, 0, len(g.claims))
	for cell := range g.claims {
		cells = append(cells, cell)
	}
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].Y != cells[j].Y {
			return cells[i].Y < cells[j].Y
		}
		return cells[i].X < cells[j].X
	})
	for _, cell := range cells {
		occupants := g.claims[cell]
		units := make([]string, 0, len(occupants))
		for unit := range occupants {
			units = append(units, unit)
		}
		sort.Strings(units)
		for _, unit := range units {
			snap.Claims = append(snap.Claims, snapshotClaim{Cell: cell, UnitID: unit, Priority: occupants[unit]})
		}
	}

	units := make([]string, 0, len(g.paths))
	for unit := range g.paths {
		units = append(units, unit)
	}
	sort.Strings(units)
	for _, unit := range units {
		cached := g.paths[unit]
		snap.Paths = append(snap.Paths, snapshotPath{Request: cached.Request, Result: cloneResult(cached.Result)})
	}

	return json.Marshal(snap)
}

// Restore replaces the guard state with a snapshot, all or nothing.
func (g *NavmeshCrowdGuard) Restore(data []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var snap guardSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != snapshotVersion ||
		!validGrid(snap.Grid) ||
		!validConfig(snap.Config) ||
		len(snap.Obstacles) > snap.Config.MaxObstacles ||
		len(snap.Audit) > snap.Config.MaxAudit ||
		!validSnapshotStats(snap.Stats) ||
		snap.AuditSeq < 0 {
		return ErrBadSnapshot
	}

	obstacles := make(map[string]Obstacle, len(snap.Obstacles))
	dynamicBlocked := make(map[Cell]int)
	for _, obs := range snap.Obstacles {
		if strings.TrimSpace(obs.ID) == "" ||
			!validRegionShape(obs.Region) ||
			!regionInGrid(snap.Grid, obs.Region) {
			return ErrBadSnapshot
		}
		if _, ok := obstacles[obs.ID]; ok {
			return ErrBadSnapshot
		}
		if len(obstacles) >= snap.Config.MaxObstacles {
			return ErrBadSnapshot
		}
		obstacles[obs.ID] = obs
		for y := obs.Region.MinY; y <= obs.Region.MaxY; y++ {
			for x := obs.Region.MinX; x <= obs.Region.MaxX; x++ {
				dynamicBlocked[Cell{X: x, Y: y}]++
			}
		}
	}

	claims := make(map[Cell]map[string]int)
	for _, claim := range snap.Claims {
		if strings.TrimSpace(claim.UnitID) == "" || !inGrid(snap.Grid, claim.Cell) {
			return ErrBadSnapshot
		}
		if staticBlocked(snap.Grid, claim.Cell) {
			return ErrBadSnapshot
		}
		occupants := claims[claim.Cell]
		if occupants == nil {
			occupants = make(map[string]int)
			claims[claim.Cell] = occupants
		}
		if _, ok := occupants[claim.UnitID]; ok {
			return ErrBadSnapshot
		}
		occupants[claim.UnitID] = claim.Priority
	}

	paths := make(map[string]cachedPath)
	seenPathUnits := make(map[string]struct{})
	for _, sp := range snap.Paths {
		req := sp.Request
		res := sp.Result
		if strings.TrimSpace(req.UnitID) == "" || strings.TrimSpace(res.UnitID) == "" ||
			req.UnitID != res.UnitID ||
			!inGrid(snap.Grid, req.From) || !inGrid(snap.Grid, req.To) ||
			len(res.Points) == 0 || res.Cost < 0 || res.Cost != len(res.Points)-1 ||
			!inGrid(snap.Grid, res.Nearest) {
			return ErrBadSnapshot
		}
		if _, ok := seenPathUnits[req.UnitID]; ok {
			return ErrBadSnapshot
		}
		for _, c := range res.Points {
			if !inGrid(snap.Grid, c) || staticBlocked(snap.Grid, c) {
				return ErrBadSnapshot
			}
		}
		if res.Reached {
			if res.Partial || res.Points[len(res.Points)-1] != req.To {
				return ErrBadSnapshot
			}
		} else if !res.Partial || res.Points[len(res.Points)-1] != res.Nearest {
			return ErrBadSnapshot
		}
		res.Points = append([]Cell(nil), res.Points...)
		seenPathUnits[req.UnitID] = struct{}{}
		paths[req.UnitID] = cachedPath{Request: req, Result: res}
	}

	var previousSeq int64
	for i, entry := range snap.Audit {
		if entry.Seq <= 0 || (i > 0 && entry.Seq <= previousSeq) || entry.Seq > snap.AuditSeq {
			return ErrBadSnapshot
		}
		previousSeq = entry.Seq
	}

	g.grid = copyGrid(snap.Grid)
	g.cfg = snap.Config
	g.degraded = snap.Degraded
	g.obstacles = obstacles
	g.dynamicBlocked = dynamicBlocked
	g.claims = claims
	g.paths = paths
	g.stats = snap.Stats
	g.audit = append([]AuditEntry(nil), snap.Audit...)
	g.auditSeq = snap.AuditSeq
	return nil
}

func validSnapshotStats(stats GuardStats) bool {
	return stats.Queries >= 0 &&
		stats.Paths >= 0 &&
		stats.Recomputes >= 0 &&
		stats.Claims >= 0 &&
		stats.Releases >= 0 &&
		stats.Degraded >= 0
}
