package types

import (
	"encoding/json"
	"io"

	"go.skia.org/infra/go/paramtools"
	"go.skia.org/infra/go/tiling"
)

// ComplexTile contains an enriched version of a tile loaded through the ingestion process.
// It provides ways to handle sparse tiles, where many commits of the underlying raw tile
// contain no data and therefore removed.
// In either case (sparse or dense tile) it offers two versions of the tile.
// one with all ignored traces and one without the ignored traces.
// In addition it also contains the ignore rules and information about the larger "sparse" tile
// if the tiles at hand were condensed from a larger tile.
type ComplexTile interface {
	// AllCommits returns all commits that were processed to get the data commits.
	// Its first commit should match the first commit returned when calling DataCommits.
	AllCommits() []*tiling.Commit

	// DataCommits returns all commits that contain data. In some busy repos, there are commits that
	// don't get tested directly because the commits are batched in with others. DataCommits
	// is a way to get just the commits where some data has been ingested.
	DataCommits() []*tiling.Commit

	// FromSame returns true if the given complex tile was derived from the same tile as the one
	// provided and if none of the other parameters changed, especially the ignore revision.
	FromSame(completeTile *tiling.Tile, ignoreRev int64) bool

	// FilledCommits returns how many commits in the tile have data.
	FilledCommits() int

	// GetTile returns a simple tile either with or without ignored traces depending on the argument.
	GetTile(is IgnoreState) *tiling.Tile

	// SetIgnoreRules adds ignore rules to the tile and a sub-tile with the ignores removed.
	// In other words this function assumes that original tile has been filtered by the
	// ignore rules that are being passed.
	SetIgnoreRules(reducedTile *tiling.Tile, ignoreRules paramtools.ParamMatcher, irRev int64)

	// IgnoreRules returns the ignore rules for this tile.
	IgnoreRules() paramtools.ParamMatcher

	// SetSparse sets sparsity information about this tile.
	SetSparse(sparseCommits []*tiling.Commit, cardinalities []int)
}

type ComplexTileImpl struct {
	// tileExcludeIgnoredTraces is the current tile without ignored traces.
	tileExcludeIgnoredTraces *tiling.Tile

	// tileIncludeIgnoredTraces is the current tile containing all available data.
	tileIncludeIgnoredTraces *tiling.Tile

	// ignoreRules contains the rules used to created the TileWithIgnores.
	ignoreRules paramtools.ParamMatcher

	// irRevision is the (monotonically increasing) revision of the ignore rules.
	irRevision int64

	// sparseCommits are all the commits that were used condense the underlying tile.
	sparseCommits []*tiling.Commit

	// cards captures the cardinality of each commit in sparse tile, meaning how many data points
	// each commit contains.
	cardinalities []int

	// filled contains the number of commits that are non-empty.
	filled int
}

func NewComplexTile(completeTile *tiling.Tile) *ComplexTileImpl {
	return &ComplexTileImpl{
		tileExcludeIgnoredTraces: completeTile,
		tileIncludeIgnoredTraces: completeTile,
	}
}

// SetIgnoreRules fulfills the ComplexTile interface.
func (c *ComplexTileImpl) SetIgnoreRules(reducedTile *tiling.Tile, ignoreRules paramtools.ParamMatcher, irRev int64) {
	c.tileExcludeIgnoredTraces = reducedTile
	c.irRevision = irRev
	c.ignoreRules = ignoreRules
}

// SetSparse fulfills the ComplexTile interface.
func (c *ComplexTileImpl) SetSparse(sparseCommits []*tiling.Commit, cardinalities []int) {
	// Make sure we always have valid values sparce commits.
	if len(sparseCommits) == 0 {
		sparseCommits = c.tileIncludeIgnoredTraces.Commits
	}

	filled := len(c.tileIncludeIgnoredTraces.Commits)
	if len(cardinalities) == 0 {
		cardinalities = make([]int, len(sparseCommits))
		for idx := range cardinalities {
			cardinalities[idx] = len(c.tileIncludeIgnoredTraces.Traces)
		}
	} else {
		for _, card := range cardinalities {
			if card > 0 {
				filled++
			}
		}
	}

	commitsLen := tiling.LastCommitIndex(sparseCommits) + 1
	if commitsLen < len(sparseCommits) {
		sparseCommits = sparseCommits[:commitsLen]
		cardinalities = cardinalities[:commitsLen]
	}
	c.sparseCommits = sparseCommits
	c.cardinalities = cardinalities
	c.filled = filled
}

// FilledCommits fulfills the ComplexTile interface.
func (c *ComplexTileImpl) FilledCommits() int {
	return c.filled
}

// FromSame fulfills the ComplexTile interface.
func (c *ComplexTileImpl) FromSame(completeTile *tiling.Tile, ignoreRev int64) bool {
	return c != nil &&
		c.tileIncludeIgnoredTraces != nil &&
		c.tileIncludeIgnoredTraces == completeTile &&
		c.tileExcludeIgnoredTraces != nil &&
		c.irRevision == ignoreRev
}

// DataCommits fulfills the ComplexTile interface.
func (c *ComplexTileImpl) DataCommits() []*tiling.Commit {
	return c.tileIncludeIgnoredTraces.Commits
}

// AllCommits fulfills the ComplexTile interface.
func (c *ComplexTileImpl) AllCommits() []*tiling.Commit {
	return c.sparseCommits
}

// GetTile fulfills the ComplexTile interface.
func (c *ComplexTileImpl) GetTile(is IgnoreState) *tiling.Tile {
	if is == IncludeIgnoredTraces {
		return c.tileIncludeIgnoredTraces
	}
	return c.tileExcludeIgnoredTraces
}

// IgnoreRules fulfills the ComplexTile interface.
func (c *ComplexTileImpl) IgnoreRules() paramtools.ParamMatcher {
	return c.ignoreRules
}

// Make sure ComplexTileImpl fulfills the ComplexTile Interface
var _ ComplexTile = (*ComplexTileImpl)(nil)

// Same as Tile but instead of Traces we preserve the raw JSON. This is a
// utility struct that is used to parse a tile where we don't know the
// Trace type upfront.
type TileWithRawTraces struct {
	Traces    map[tiling.TraceId]json.RawMessage `json:"traces"`
	ParamSet  map[string][]string                `json:"param_set"`
	Commits   []*tiling.Commit                   `json:"commits"`
	Scale     int                                `json:"scale"`
	TileIndex int                                `json:"tileIndex"`
}

// TileFromJson parses a tile that has been serialized to JSON.
// traceExample has to be an instance of the Trace implementation
// that needs to be deserialized.
// Note: Instead of the type switch below we could use reflection
// to be truly generic, but it makes the code harder to read and
// currently we only have two types.
func TileFromJson(r io.Reader, traceExample tiling.Trace) (*tiling.Tile, error) {
	factory := func() tiling.Trace { return NewGoldenTrace() }

	// Decode everything, but the traces.
	dec := json.NewDecoder(r)
	var rawTile TileWithRawTraces
	err := dec.Decode(&rawTile)
	if err != nil {
		return nil, err
	}

	// Parse the traces.
	traces := map[tiling.TraceId]tiling.Trace{}
	for k, rawJson := range rawTile.Traces {
		newTrace := factory()
		if err = json.Unmarshal(rawJson, newTrace); err != nil {
			return nil, err
		}
		traces[k] = newTrace.(tiling.Trace)
	}

	return &tiling.Tile{
		Traces:    traces,
		ParamSet:  rawTile.ParamSet,
		Commits:   rawTile.Commits,
		Scale:     rawTile.Scale,
		TileIndex: rawTile.Scale,
	}, nil
}