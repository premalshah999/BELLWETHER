package forecast

import (
	"math"
	"math/rand"
	"sort"
)

// GBDT is a gradient-boosted ensemble of shallow regression trees on binned
// features: the same algorithm as LightGBM's histogram method, which Qlib's
// published benchmarks found the strongest of its classical models on
// cross-sectional stock factors. Trees find what a linear model cannot:
// that reversal works in liquid stocks and not illiquid ones, that momentum
// crashes when market volatility is high (Daniel and Moskowitz, 2016).
//
// It is regularised hard because the target is mostly noise: depth three,
// thousands of rows per leaf, half the rows and two thirds of the features
// per tree, a small learning rate, and the number of trees chosen on a later
// slice of the training window it never fitted.
type GBDT struct {
	Edges [][]float32 `json:"-"`
	Trees []gbTree    `json:"-"`
	Base  float64     `json:"base"`
	LR    float64     `json:"lr"`
	// Used is how many trees early stopping kept.
	Used int `json:"trees"`
	// Gain is each feature's total split gain: what the model leaned on.
	Gain []float64 `json:"gain"`
}

type gbTree struct {
	feat  []int16
	bin   []uint8
	left  []int32
	right []int32
	value []float64
}

const nBins = 32

type gbConfig struct {
	Trees, Depth, MinLeaf int
	LR, Lambda            float64
	RowFrac, FeatFrac     float64
	Seed                  int64
}

var defaultGB = gbConfig{Trees: 300, Depth: 3, MinLeaf: 800, LR: 0.04, Lambda: 50, RowFrac: 0.5, FeatFrac: 0.66, Seed: 7}

// binEdges are each feature's quantile cut points on the training rows.
func binEdges(x [][]float32, nf int) [][]float32 {
	edges := make([][]float32, nf)
	vals := make([]float32, 0, len(x))
	for f := 0; f < nf; f++ {
		vals = vals[:0]
		for i := 0; i < len(x); i += max(1, len(x)/20000) {
			vals = append(vals, x[i][f])
		}
		sort.Slice(vals, func(a, b int) bool { return vals[a] < vals[b] })
		var e []float32
		for k := 1; k < nBins; k++ {
			v := vals[k*len(vals)/nBins]
			if len(e) == 0 || v > e[len(e)-1] {
				e = append(e, v)
			}
		}
		edges[f] = e
	}
	return edges
}

func binOf(edges []float32, v float32) uint8 {
	return uint8(sort.Search(len(edges), func(i int) bool { return edges[i] > v }))
}

func binRows(x [][]float32, edges [][]float32) [][]uint8 {
	out := make([][]uint8, len(x))
	for i, row := range x {
		b := make([]uint8, len(edges))
		for f := range edges {
			b[f] = binOf(edges[f], row[f])
		}
		out[i] = b
	}
	return out
}

// fitGBDT trains on rows [0, split) and stops early on [split, n).
func fitGBDT(x [][]float32, y []float64, split int, cfg gbConfig) GBDT {
	nf := len(x[0])
	edges := binEdges(x[:split], nf)
	bx := binRows(x, edges)
	m := GBDT{Edges: edges, LR: cfg.LR, Gain: make([]float64, nf)}
	var base float64
	for _, v := range y[:split] {
		base += v
	}
	m.Base = base / float64(split)
	pred := make([]float64, len(y))
	for i := range pred {
		pred[i] = m.Base
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	bestLoss, bestN := math.Inf(1), 0
	valLoss := func() float64 {
		var s float64
		for i := split; i < len(y); i++ {
			d := y[i] - pred[i]
			s += d * d
		}
		return s / float64(max(1, len(y)-split))
	}
	grad := make([]float64, split)
	rows := make([]int32, 0, split)
	gains := make([]float64, nf)
	for t := 0; t < cfg.Trees; t++ {
		for i := 0; i < split; i++ {
			grad[i] = y[i] - pred[i]
		}
		rows = rows[:0]
		for i := 0; i < split; i++ {
			if rng.Float64() < cfg.RowFrac {
				rows = append(rows, int32(i))
			}
		}
		var feats []int
		for f := 0; f < nf; f++ {
			if rng.Float64() < cfg.FeatFrac {
				feats = append(feats, f)
			}
		}
		if len(feats) == 0 {
			feats = []int{rng.Intn(nf)}
		}
		for f := range gains {
			gains[f] = 0
		}
		tr := growTree(bx, grad, rows, feats, cfg, gains)
		for i := range pred {
			pred[i] += cfg.LR * tr.predict(bx[i])
		}
		m.Trees = append(m.Trees, tr)
		for f := range gains {
			m.Gain[f] += gains[f]
		}
		if split < len(y) {
			if l := valLoss(); l < bestLoss {
				bestLoss, bestN = l, t+1
			}
		} else {
			bestN = t + 1
		}
	}
	if bestN == 0 {
		bestN = 1
	}
	m.Trees = m.Trees[:bestN]
	m.Used = bestN
	return m
}

type node struct {
	rows  []int32
	depth int
	id    int32
}

func growTree(bx [][]uint8, grad []float64, rows []int32, feats []int, cfg gbConfig, gains []float64) gbTree {
	var tr gbTree
	add := func() int32 {
		tr.feat = append(tr.feat, -1)
		tr.bin = append(tr.bin, 0)
		tr.left = append(tr.left, -1)
		tr.right = append(tr.right, -1)
		tr.value = append(tr.value, 0)
		return int32(len(tr.feat) - 1)
	}
	root := node{rows: rows, depth: 0, id: add()}
	queue := []node{root}
	var hg [nBins]float64
	var hn [nBins]int
	for len(queue) > 0 {
		nd := queue[0]
		queue = queue[1:]
		var g float64
		for _, r := range nd.rows {
			g += grad[r]
		}
		n := float64(len(nd.rows))
		tr.value[nd.id] = g / (n + cfg.Lambda)
		if nd.depth >= cfg.Depth || len(nd.rows) < 2*cfg.MinLeaf {
			continue
		}
		parent := g * g / (n + cfg.Lambda)
		bestGain, bestF, bestB := 0.0, -1, 0
		for _, f := range feats {
			hg, hn = [nBins]float64{}, [nBins]int{}
			for _, r := range nd.rows {
				b := bx[r][f]
				hg[b] += grad[r]
				hn[b]++
			}
			var gl float64
			nl := 0
			for b := 0; b < nBins-1; b++ {
				gl += hg[b]
				nl += hn[b]
				nr := len(nd.rows) - nl
				if nl < cfg.MinLeaf || nr < cfg.MinLeaf {
					continue
				}
				gr := g - gl
				gain := gl*gl/(float64(nl)+cfg.Lambda) + gr*gr/(float64(nr)+cfg.Lambda) - parent
				if gain > bestGain {
					bestGain, bestF, bestB = gain, f, b
				}
			}
		}
		if bestF < 0 {
			continue
		}
		gains[bestF] += bestGain
		var lr, rr []int32
		for _, r := range nd.rows {
			if int(bx[r][bestF]) <= bestB {
				lr = append(lr, r)
			} else {
				rr = append(rr, r)
			}
		}
		tr.feat[nd.id], tr.bin[nd.id] = int16(bestF), uint8(bestB)
		l, rgt := add(), add()
		tr.left[nd.id], tr.right[nd.id] = l, rgt
		queue = append(queue, node{rows: lr, depth: nd.depth + 1, id: l}, node{rows: rr, depth: nd.depth + 1, id: rgt})
	}
	return tr
}

func (tr gbTree) predict(b []uint8) float64 {
	i := int32(0)
	for tr.feat[i] >= 0 {
		if b[tr.feat[i]] <= tr.bin[i] {
			i = tr.left[i]
		} else {
			i = tr.right[i]
		}
	}
	return tr.value[i]
}

// Predict scores one row of raw features.
func (m GBDT) Predict(x []float32) float64 {
	b := make([]uint8, len(m.Edges))
	for f := range m.Edges {
		b[f] = binOf(m.Edges[f], x[f])
	}
	s := m.Base
	for _, t := range m.Trees {
		s += m.LR * t.predict(b)
	}
	return s
}

// contributions splits one prediction across the features the trees split
// on (Saabas): each step down a tree moves the prediction from the parent's
// value to the child's, and that change is credited to the split feature.
func (m GBDT) contributions(x []float32) []float64 {
	out := make([]float64, len(m.Edges))
	b := make([]uint8, len(m.Edges))
	for f := range m.Edges {
		b[f] = binOf(m.Edges[f], x[f])
	}
	for _, tr := range m.Trees {
		i := int32(0)
		for tr.feat[i] >= 0 {
			next := tr.right[i]
			if b[tr.feat[i]] <= tr.bin[i] {
				next = tr.left[i]
			}
			out[tr.feat[i]] += m.LR * (tr.value[next] - tr.value[i])
			i = next
		}
	}
	return out
}
