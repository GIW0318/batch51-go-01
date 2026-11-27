// Package task builds a volumetric cloud density field from a weather map,
// plans per-row ray march step ranges and reuses shadow samples keyed by light
// direction and depth bucket.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/bits"
	"sort"
	"sync"
)

// Errors returned by the volume.
var (
	ErrBadConfig   = errors.New("cloud config is invalid")
	ErrBadWeather  = errors.New("weather map is invalid")
	ErrBadLight    = errors.New("light direction is invalid")
	ErrNotBuilt    = errors.New("cloud volume is not built")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

const (
	volumeResolution = 4
	snapshotVersion  = 1
)

// Vec3 is a point or a direction in world space.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Bounds is an axis aligned box.
type Bounds struct {
	Min Vec3 `json:"min"`
	Max Vec3 `json:"max"`
}

// Camera is the view the volume is built for.
type Camera struct {
	Position Vec3    `json:"position"`
	Forward  Vec3    `json:"forward"`
	FovDeg   float64 `json:"fov_deg"`
}

// Viewport is the screen target of one frame.
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// WeatherMap is a row major coverage grid laid over a rectangle.
type WeatherMap struct {
	Width    int       `json:"width"`
	Height   int       `json:"height"`
	ExtentX  float64   `json:"extent_x"`
	ExtentY  float64   `json:"extent_y"`
	Coverage []float64 `json:"coverage"`
}

// CloudConfig is the static description of the cloud layer.
type CloudConfig struct {
	BaseAltitude      float64 `json:"base_altitude"`
	TopAltitude       float64 `json:"top_altitude"`
	CoverageThreshold float64 `json:"coverage_threshold"`
	MaxSteps          int     `json:"max_steps"`
	PlanRows          int     `json:"plan_rows"`
	ShadowBuckets     int     `json:"shadow_buckets"`
	AngleToleranceDeg float64 `json:"angle_tolerance_deg"`
}

// Volume is the built density field summary.
type Volume struct {
	Bounds     Bounds    `json:"bounds"`
	Coverage   float64   `json:"coverage"`
	Threshold  float64   `json:"threshold"`
	Resolution int       `json:"resolution"`
	Cells      []float64 `json:"cells"`
}

// StepRange is one screen row worth of ray march work.
type StepRange struct {
	Row   int     `json:"row"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Steps int     `json:"steps"`
}

// ShadowStats counts shadow cache traffic.
type ShadowStats struct {
	Hits        int `json:"hits"`
	Misses      int `json:"misses"`
	Invalidated int `json:"invalidated"`
}

type shadowBucketKey struct {
	positionX int
	positionY int
	positionZ int
	lightX    int
	lightY    int
	lightZ    int
}

type shadowEntry struct {
	position      Vec3
	direction     Vec3
	transmittance float64
}

// CloudVolume owns the density field, the step plan and the shadow cache.
type CloudVolume struct {
	mu sync.RWMutex

	cfg CloudConfig

	weather WeatherMap
	camera  Camera

	built     bool
	bounds    Bounds
	coverage  float64
	threshold float64
	cells     []float64

	shadowCache map[shadowBucketKey]shadowEntry
	shadowStats ShadowStats
}

// NewCloudVolume builds a cloud volume with the given config.
func NewCloudVolume(cfg CloudConfig) *CloudVolume {
	return &CloudVolume{
		cfg:         cfg,
		shadowCache: make(map[shadowBucketKey]shadowEntry),
	}
}

// Build validates the weather map and camera and stores the density field.
func (c *CloudVolume) Build(weather WeatherMap, camera Camera) (Volume, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := validateConfig(c.cfg); err != nil {
		return Volume{}, err
	}
	if err := validateWeather(weather); err != nil {
		return Volume{}, err
	}

	weather = cloneWeather(weather)
	bounds := Bounds{
		Min: Vec3{X: 0, Y: 0, Z: c.cfg.BaseAltitude},
		Max: Vec3{X: weather.ExtentX, Y: weather.ExtentY, Z: c.cfg.TopAltitude},
	}
	cells := buildCells(weather, bounds, c.cfg.CoverageThreshold)
	coverage := meanCoverage(weather)

	// Commit only after every value has been validated and prepared. A failed
	// build therefore cannot expose a partially replaced density field.
	c.weather = weather
	c.camera = camera
	c.built = true
	c.bounds = bounds
	c.coverage = coverage
	c.threshold = c.cfg.CoverageThreshold
	c.cells = cells
	c.shadowCache = make(map[shadowBucketKey]shadowEntry)
	c.shadowStats = ShadowStats{}

	return Volume{
		Bounds:     bounds,
		Coverage:   coverage,
		Threshold:  c.threshold,
		Resolution: volumeResolution,
		Cells:      append([]float64(nil), cells...),
	}, nil
}

// PlanSteps splits the viewport into rows and distributes the step budget.
func (c *CloudVolume) PlanSteps(view Viewport, budget int) []StepRange {
	c.mu.RLock()
	if !c.built {
		c.mu.RUnlock()
		return nil
	}
	rows := c.cfg.PlanRows
	maxSteps := c.cfg.MaxSteps
	c.mu.RUnlock()

	if budget <= 0 {
		budget = maxSteps
	}

	plan := make([]StepRange, rows)
	for i := 0; i < rows; i++ {
		plan[i] = StepRange{
			Row:   i,
			Start: float64(i) / float64(rows),
			End:   float64(i+1) / float64(rows),
		}
	}

	if budget <= rows {
		for i := 0; i < budget; i++ {
			plan[i].Steps = 1
		}
		return plan
	}

	for i := range plan {
		plan[i].Steps = 1
	}

	remaining := int64(budget - rows)
	totalWeight := int64(rows) * (int64(rows) + 1) / 2
	var allocated int64
	for i := 0; i < rows; i++ {
		weight := int64(rows - i)
		extra := mulDivPositive(remaining, weight, totalWeight)
		plan[i].Steps += int(extra)
		allocated += extra
	}

	// The weighted floors leave fewer than one step per row unassigned. Give
	// those steps to the nearest rows first so the total is exactly the budget.
	for left := remaining - allocated; left > 0; left-- {
		index := int((remaining - left) % int64(rows))
		plan[index].Steps++
	}

	return plan
}

// SampleShadow returns the transmittance towards the light at one position.
func (c *CloudVolume) SampleShadow(pos Vec3, lightDir Vec3) (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.built {
		return 0, ErrNotBuilt
	}

	direction, ok := normalize(lightDir)
	if !ok {
		return 0, ErrBadLight
	}

	key := c.shadowKey(pos, direction)
	if entry, hit := c.shadowCache[key]; hit {
		if entry.direction == direction || angleDegrees(entry.direction, direction) <= c.cfg.AngleToleranceDeg {
			c.shadowStats.Hits++
			return entry.transmittance, nil
		}

		// The direction bucket reused an entry that is outside the configured
		// angular tolerance. Drop the whole bucket and sample with the new light.
		for candidate := range c.shadowCache {
			if candidate.positionX == key.positionX &&
				candidate.positionY == key.positionY &&
				candidate.positionZ == key.positionZ &&
				candidate.lightX == key.lightX &&
				candidate.lightY == key.lightY &&
				candidate.lightZ == key.lightZ {
				delete(c.shadowCache, candidate)
				c.shadowStats.Invalidated++
			}
		}
	}

	c.shadowStats.Misses++

	thickness := c.bounds.Max.Z - c.bounds.Min.Z
	step := thickness / 8
	sample := Vec3{
		X: pos.X + direction.X*step,
		Y: pos.Y + direction.Y*step,
		Z: pos.Z + direction.Z*step,
	}
	density := densityAtWeather(c.weather, c.bounds, c.threshold, sample)
	transmittance := 1 / (1 + density)

	c.shadowCache[key] = shadowEntry{
		position:      pos,
		direction:     direction,
		transmittance: transmittance,
	}
	return transmittance, nil
}

// InvalidateRegion drops every shadow cache entry inside the box.
func (c *CloudVolume) InvalidateRegion(bounds Bounds) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.built {
		return ErrNotBuilt
	}
	if bounds.Max.X < bounds.Min.X || bounds.Max.Y < bounds.Min.Y || bounds.Max.Z < bounds.Min.Z {
		return nil
	}

	for key, entry := range c.shadowCache {
		if pointInBounds(entry.position, bounds) {
			delete(c.shadowCache, key)
			c.shadowStats.Invalidated++
		}
	}
	return nil
}

// DensityAt samples the density field at a world position.
func (c *CloudVolume) DensityAt(pos Vec3) (float64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.built {
		return 0, ErrNotBuilt
	}
	return densityAtWeather(c.weather, c.bounds, c.threshold, pos), nil
}

// CoverageAt interpolates the weather map at a horizontal position.
func (c *CloudVolume) CoverageAt(x, y float64) (float64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.built {
		return 0, ErrNotBuilt
	}
	return coverageAtWeather(c.weather, x, y), nil
}

// CloudBounds returns the built volume bounds.
func (c *CloudVolume) CloudBounds() (Bounds, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.built {
		return Bounds{}, false
	}
	return c.bounds, true
}

// ShadowCache reports the cache counters.
func (c *CloudVolume) ShadowCache() ShadowStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.shadowStats
}

type snapshotShadowEntry struct {
	Position      Vec3    `json:"position"`
	Direction     Vec3    `json:"direction"`
	Transmittance float64 `json:"transmittance"`
}

type snapshotState struct {
	Config  CloudConfig           `json:"config"`
	Weather WeatherMap            `json:"weather"`
	Camera  Camera                `json:"camera"`
	Built   bool                  `json:"built"`
	Stats   ShadowStats           `json:"stats"`
	Cache   []snapshotShadowEntry `json:"cache,omitempty"`
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

// Snapshot serialises the weather map, camera and cache counters.
func (c *CloudVolume) Snapshot() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	state := snapshotState{
		Config:  c.cfg,
		Weather: cloneWeather(c.weather),
		Camera:  c.camera,
		Built:   c.built,
		Stats:   c.shadowStats,
	}
	if c.built {
		state.Cache = make([]snapshotShadowEntry, 0, len(c.shadowCache))
		for _, entry := range c.shadowCache {
			state.Cache = append(state.Cache, snapshotShadowEntry{
				Position:      entry.position,
				Direction:     entry.direction,
				Transmittance: entry.transmittance,
			})
		}
		sort.Slice(state.Cache, func(i, j int) bool {
			a, b := state.Cache[i], state.Cache[j]
			if a.Position.X != b.Position.X {
				return a.Position.X < b.Position.X
			}
			if a.Position.Y != b.Position.Y {
				return a.Position.Y < b.Position.Y
			}
			if a.Position.Z != b.Position.Z {
				return a.Position.Z < b.Position.Z
			}
			if a.Direction.X != b.Direction.X {
				return a.Direction.X < b.Direction.X
			}
			if a.Direction.Y != b.Direction.Y {
				return a.Direction.Y < b.Direction.Y
			}
			return a.Direction.Z < b.Direction.Z
		})
	}

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope, err := json.Marshal(snapshotEnvelope{
		Version:  snapshotVersion,
		Payload:  payload,
		Checksum: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return nil, err
	}
	return envelope, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *CloudVolume) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version != snapshotVersion || len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.Payload, &state); err != nil {
		return ErrBadSnapshot
	}
	if err := validateConfig(state.Config); err != nil {
		return ErrBadSnapshot
	}
	if state.Stats.Hits < 0 || state.Stats.Misses < 0 || state.Stats.Invalidated < 0 {
		return ErrBadSnapshot
	}

	next := CloudVolume{
		cfg:         state.Config,
		weather:     cloneWeather(state.Weather),
		camera:      state.Camera,
		built:       state.Built,
		shadowCache: make(map[shadowBucketKey]shadowEntry),
		shadowStats: state.Stats,
	}
	if state.Built {
		if err := validateWeather(state.Weather); err != nil {
			return ErrBadSnapshot
		}
		next.bounds = Bounds{
			Min: Vec3{X: 0, Y: 0, Z: state.Config.BaseAltitude},
			Max: Vec3{X: state.Weather.ExtentX, Y: state.Weather.ExtentY, Z: state.Config.TopAltitude},
		}
		next.coverage = meanCoverage(state.Weather)
		next.threshold = state.Config.CoverageThreshold
		next.cells = buildCells(state.Weather, next.bounds, next.threshold)

		for _, entry := range state.Cache {
			if !finite(entry.Position.X) || !finite(entry.Position.Y) || !finite(entry.Position.Z) ||
				!finite(entry.Transmittance) || entry.Transmittance < 0 || entry.Transmittance > 1 {
				return ErrBadSnapshot
			}
			direction, ok := normalize(entry.Direction)
			if !ok {
				return ErrBadSnapshot
			}
			key := shadowKeyFor(next.bounds, state.Config.ShadowBuckets, entry.Position, direction)
			next.shadowCache[key] = shadowEntry{
				position:      entry.Position,
				direction:     direction,
				transmittance: entry.Transmittance,
			}
		}
	} else if len(state.Cache) != 0 {
		return ErrBadSnapshot
	}

	c.mu.Lock()
	c.cfg = next.cfg
	c.weather = next.weather
	c.camera = next.camera
	c.built = next.built
	c.bounds = next.bounds
	c.coverage = next.coverage
	c.threshold = next.threshold
	c.cells = next.cells
	c.shadowCache = next.shadowCache
	c.shadowStats = next.shadowStats
	c.mu.Unlock()
	return nil
}

func validateConfig(cfg CloudConfig) error {
	if !finite(cfg.BaseAltitude) || !finite(cfg.TopAltitude) || !(cfg.TopAltitude > cfg.BaseAltitude) {
		return ErrBadConfig
	}
	if !finite(cfg.CoverageThreshold) || cfg.CoverageThreshold < 0 || cfg.CoverageThreshold > 1 {
		return ErrBadConfig
	}
	if cfg.MaxSteps <= 0 || cfg.PlanRows <= 0 || cfg.ShadowBuckets <= 0 {
		return ErrBadConfig
	}
	if !finite(cfg.AngleToleranceDeg) || !(cfg.AngleToleranceDeg > 0) {
		return ErrBadConfig
	}
	return nil
}

func validateWeather(weather WeatherMap) error {
	if weather.Width <= 0 || weather.Height <= 0 {
		return ErrBadWeather
	}
	if int64(weather.Width) > math.MaxInt64/int64(weather.Height) {
		return ErrBadWeather
	}
	if int64(weather.Width)*int64(weather.Height) != int64(len(weather.Coverage)) {
		return ErrBadWeather
	}
	if !finite(weather.ExtentX) || !finite(weather.ExtentY) || !(weather.ExtentX > 0) || !(weather.ExtentY > 0) {
		return ErrBadWeather
	}
	for _, coverage := range weather.Coverage {
		if !finite(coverage) || coverage < 0 || coverage > 1 {
			return ErrBadWeather
		}
	}
	return nil
}

func cloneWeather(weather WeatherMap) WeatherMap {
	weather.Coverage = append([]float64(nil), weather.Coverage...)
	return weather
}

func meanCoverage(weather WeatherMap) float64 {
	if len(weather.Coverage) == 0 {
		return 0
	}
	var sum float64
	for _, coverage := range weather.Coverage {
		sum += coverage
	}
	return sum / float64(len(weather.Coverage))
}

func buildCells(weather WeatherMap, bounds Bounds, threshold float64) []float64 {
	cells := make([]float64, volumeResolution*volumeResolution*volumeResolution)
	index := 0
	width := bounds.Max.X - bounds.Min.X
	depth := bounds.Max.Y - bounds.Min.Y
	height := bounds.Max.Z - bounds.Min.Z
	for z := 0; z < volumeResolution; z++ {
		posZ := bounds.Min.Z + (float64(z)+0.5)*height/volumeResolution
		for y := 0; y < volumeResolution; y++ {
			posY := bounds.Min.Y + (float64(y)+0.5)*depth/volumeResolution
			for x := 0; x < volumeResolution; x++ {
				posX := bounds.Min.X + (float64(x)+0.5)*width/volumeResolution
				cells[index] = densityAtWeather(weather, bounds, threshold, Vec3{X: posX, Y: posY, Z: posZ})
				index++
			}
		}
	}
	return cells
}

func coverageAtWeather(weather WeatherMap, x, y float64) float64 {
	if math.IsNaN(x) {
		x = 0
	}
	if math.IsNaN(y) {
		y = 0
	}
	gridX := 0.0
	if weather.Width > 1 {
		gridX = x / weather.ExtentX * float64(weather.Width-1)
	}
	gridY := 0.0
	if weather.Height > 1 {
		gridY = y / weather.ExtentY * float64(weather.Height-1)
	}
	gridX = clampFloat(gridX, 0, float64(weather.Width-1))
	gridY = clampFloat(gridY, 0, float64(weather.Height-1))

	x0 := int(math.Floor(gridX))
	y0 := int(math.Floor(gridY))
	if x0 >= weather.Width {
		x0 = weather.Width - 1
	}
	if y0 >= weather.Height {
		y0 = weather.Height - 1
	}
	x1 := x0 + 1
	if x1 >= weather.Width {
		x1 = weather.Width - 1
	}
	y1 := y0 + 1
	if y1 >= weather.Height {
		y1 = weather.Height - 1
	}
	tx := gridX - float64(x0)
	ty := gridY - float64(y0)

	c00 := weather.Coverage[y0*weather.Width+x0]
	c10 := weather.Coverage[y0*weather.Width+x1]
	c01 := weather.Coverage[y1*weather.Width+x0]
	c11 := weather.Coverage[y1*weather.Width+x1]
	top := c00 + (c10-c00)*tx
	bottom := c01 + (c11-c01)*tx
	return top + (bottom-top)*ty
}

func densityAtWeather(weather WeatherMap, bounds Bounds, threshold float64, pos Vec3) float64 {
	if !finite(pos.X) || !finite(pos.Y) || !finite(pos.Z) {
		return 0
	}
	if !(pos.Z > bounds.Min.Z && pos.Z < bounds.Max.Z) {
		return 0
	}

	coverage := coverageAtWeather(weather, pos.X, pos.Y)
	if coverage < threshold {
		return 0
	}

	height := (pos.Z - bounds.Min.Z) / (bounds.Max.Z - bounds.Min.Z)
	heightShape := math.Sin(math.Pi * height)
	if !(heightShape > 0) {
		return 0
	}

	covered := 0.0
	if threshold < 1 {
		if coverage > threshold {
			covered = (coverage - threshold) / (1 - threshold)
		}
	} else {
		covered = 1
	}
	covered = clampFloat(covered, 0, 1)

	noise := cloudNoise(pos)
	density := heightShape * (0.25 + 0.75*covered) * (0.18 + 0.82*noise)
	return clampFloat(density, 0, 1)
}

func cloudNoise(pos Vec3) float64 {
	first := valueNoise3(pos.X*0.045+17.1, pos.Y*0.045-9.7, pos.Z*0.080+3.3)
	second := valueNoise3(pos.X*0.110-4.2, pos.Y*0.110+8.4, pos.Z*0.170-1.3)
	return clampFloat(first*0.7+second*0.3, 0, 1)
}

func valueNoise3(x, y, z float64) float64 {
	const coordinateLimit = 1.0e15
	x = clampFloat(x, -coordinateLimit, coordinateLimit)
	y = clampFloat(y, -coordinateLimit, coordinateLimit)
	z = clampFloat(z, -coordinateLimit, coordinateLimit)

	xf := math.Floor(x)
	yf := math.Floor(y)
	zf := math.Floor(z)
	x0, y0, z0 := int64(xf), int64(yf), int64(zf)
	tx := smoothstep(x - xf)
	ty := smoothstep(y - yf)
	tz := smoothstep(z - zf)

	c000 := latticeValue(x0, y0, z0)
	c100 := latticeValue(x0+1, y0, z0)
	c010 := latticeValue(x0, y0+1, z0)
	c110 := latticeValue(x0+1, y0+1, z0)
	c001 := latticeValue(x0, y0, z0+1)
	c101 := latticeValue(x0+1, y0, z0+1)
	c011 := latticeValue(x0, y0+1, z0+1)
	c111 := latticeValue(x0+1, y0+1, z0+1)

	x00 := c000 + (c100-c000)*tx
	x10 := c010 + (c110-c010)*tx
	x01 := c001 + (c101-c001)*tx
	x11 := c011 + (c111-c011)*tx
	y0v := x00 + (x10-x00)*ty
	y1v := x01 + (x11-x01)*ty
	return y0v + (y1v-y0v)*tz
}

func smoothstep(v float64) float64 {
	return v * v * (3 - 2*v)
}

func latticeValue(x, y, z int64) float64 {
	hash := uint64(x) * 0x9e3779b97f4a7c15
	hash ^= uint64(y) * 0xc2b2ae3d27d4eb4f
	hash ^= uint64(z) * 0x165667b19e3779f9
	hash = mix64(hash)
	return float64(hash>>11) * (1.0 / 9007199254740992.0)
}

func mix64(value uint64) uint64 {
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func clampFloat(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func mulDivPositive(value, factor, divisor int64) int64 {
	if value == 0 || factor == 0 {
		return 0
	}
	high, low := bits.Mul64(uint64(value), uint64(factor))
	quotient, _ := bits.Div64(high, low, uint64(divisor))
	return int64(quotient)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func normalize(v Vec3) (Vec3, bool) {
	if !finite(v.X) || !finite(v.Y) || !finite(v.Z) {
		return Vec3{}, false
	}
	length := math.Sqrt(v.X*v.X + v.Y*v.Y + v.Z*v.Z)
	if length == 0 || !finite(length) {
		return Vec3{}, false
	}
	return Vec3{X: v.X / length, Y: v.Y / length, Z: v.Z / length}, true
}

func (c *CloudVolume) shadowKey(pos, direction Vec3) shadowBucketKey {
	return shadowKeyFor(c.bounds, c.cfg.ShadowBuckets, pos, direction)
}

func shadowKeyFor(bounds Bounds, buckets int, pos, direction Vec3) shadowBucketKey {
	bucketSizeX := (bounds.Max.X - bounds.Min.X) / float64(buckets)
	bucketSizeY := (bounds.Max.Y - bounds.Min.Y) / float64(buckets)
	bucketSizeZ := (bounds.Max.Z - bounds.Min.Z) / float64(buckets)

	return shadowBucketKey{
		positionX: bucketCoordinate(pos.X, bounds.Min.X, bucketSizeX),
		positionY: bucketCoordinate(pos.Y, bounds.Min.Y, bucketSizeY),
		positionZ: bucketCoordinate(pos.Z, bounds.Min.Z, bucketSizeZ),
		lightX:    directionCoordinate(direction.X, buckets),
		lightY:    directionCoordinate(direction.Y, buckets),
		lightZ:    directionCoordinate(direction.Z, buckets),
	}
}

func bucketCoordinate(value, minimum, size float64) int {
	if size <= 0 || !finite(size) {
		return 0
	}
	coordinate := (value - minimum) / size
	if !finite(coordinate) {
		return 0
	}
	coordinate = clampFloat(coordinate, -1.0e15, 1.0e15)
	return int(math.Floor(coordinate))
}

func directionCoordinate(value float64, buckets int) int {
	coordinate := (value + 1) * 0.5 * float64(buckets)
	if coordinate < 0 {
		coordinate = 0
	}
	if coordinate >= float64(buckets) {
		coordinate = float64(buckets - 1)
	}
	return int(math.Floor(coordinate))
}

func angleDegrees(a, b Vec3) float64 {
	dot := a.X*b.X + a.Y*b.Y + a.Z*b.Z
	dot = clampFloat(dot, -1, 1)
	return math.Acos(dot) * 180 / math.Pi
}

func pointInBounds(point Vec3, bounds Bounds) bool {
	return point.X >= bounds.Min.X && point.X <= bounds.Max.X &&
		point.Y >= bounds.Min.Y && point.Y <= bounds.Max.Y &&
		point.Z >= bounds.Min.Z && point.Z <= bounds.Max.Z
}
