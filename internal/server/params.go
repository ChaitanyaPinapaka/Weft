package server

import (
	"encoding/json"
	"net/http"
	"sync"

	"weft/internal/index"
	"weft/internal/surface"
)

const surfaceParamsKey = "surface_params"

// paramStore holds the live, runtime-tunable surfacing weights. It starts from
// surface.DefaultParams, overlays any values persisted in the index, and serves
// reads to the (concurrent) surfaceHandler under an RWMutex. Persisting to the
// index — the daemon's own rebuildable state — keeps the knobs tunable without
// an external config file (CLAUDE.md forbids those; the vault stays pure HTML).
type paramStore struct {
	mu sync.RWMutex
	p  surface.Params
	ix *index.Index
}

func newParamStore(ix *index.Index) *paramStore {
	ps := &paramStore{p: surface.DefaultParams(), ix: ix}
	if v, ok, _ := ix.GetSetting(surfaceParamsKey); ok {
		var patch paramPatch
		if json.Unmarshal([]byte(v), &patch) == nil {
			ps.p = patch.applyTo(ps.p) // overlay persisted tunables onto defaults
		}
	}
	return ps
}

func (ps *paramStore) get() surface.Params {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.p
}

// apply merges a patch (partial, or a reset), clamps to sane ranges, persists
// the tunable snapshot to the index, and returns the new full params.
func (ps *paramStore) apply(patch paramPatch) surface.Params {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if patch.Reset {
		ps.p = surface.DefaultParams()
	} else {
		ps.p = patch.applyTo(ps.p)
	}
	if blob, err := json.Marshal(toView(ps.p)); err == nil {
		_ = ps.ix.SetSetting(surfaceParamsKey, string(blob))
	}
	return ps.p
}

// paramPatch is the JSON body for POST /api/params: any subset of the nine
// tunable knobs (pointers so absent keys are left unchanged), plus reset.
type paramPatch struct {
	SpreadScale  *float64 `json:"spread_scale"`
	Decay        *float64 `json:"decay"`
	SemThreshold *float64 `json:"sem_threshold"`
	CoSaturation *float64 `json:"co_saturation"`
	WBacklink    *float64 `json:"w_backlink"`
	WSemantic    *float64 `json:"w_semantic"`
	WCoAccess    *float64 `json:"w_coaccess"`
	NoiseScale   *float64 `json:"noise_scale"`
	TopN         *int     `json:"top_n"`
	Reset        bool     `json:"reset"`
}

func (pp paramPatch) applyTo(p surface.Params) surface.Params {
	if pp.SpreadScale != nil {
		p.SpreadScale = clampF(*pp.SpreadScale, 0, 50)
	}
	if pp.Decay != nil {
		p.Decay = clampF(*pp.Decay, 0.05, 2)
	}
	if pp.SemThreshold != nil {
		p.SemThreshold = clampF(*pp.SemThreshold, 0, 1)
	}
	if pp.CoSaturation != nil {
		p.CoSaturation = clampF(*pp.CoSaturation, 1, 100)
	}
	if pp.WBacklink != nil {
		p.WBacklink = clampF(*pp.WBacklink, 0, 1)
	}
	if pp.WSemantic != nil {
		p.WSemantic = clampF(*pp.WSemantic, 0, 1)
	}
	if pp.WCoAccess != nil {
		p.WCoAccess = clampF(*pp.WCoAccess, 0, 1)
	}
	if pp.NoiseScale != nil {
		p.NoiseScale = clampF(*pp.NoiseScale, 0, 2)
	}
	if pp.TopN != nil {
		n := *pp.TopN
		if n < 1 {
			n = 1
		} else if n > 50 {
			n = 50
		}
		p.TopN = n
	}
	return p
}

// toView is the GET /api/params response: the nine tunable knobs as plain values.
func toView(p surface.Params) map[string]any {
	return map[string]any{
		"spread_scale":  p.SpreadScale,
		"decay":         p.Decay,
		"sem_threshold": p.SemThreshold,
		"co_saturation": p.CoSaturation,
		"w_backlink":    p.WBacklink,
		"w_semantic":    p.WSemantic,
		"w_coaccess":    p.WCoAccess,
		"noise_scale":   p.NoiseScale,
		"top_n":         p.TopN,
	}
}

func getParamsHandler(ps *paramStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toView(ps.get()))
	}
}

func postParamsHandler(ps *paramStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var patch paramPatch
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&patch); err != nil {
			http.Error(w, "bad JSON body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toView(ps.apply(patch)))
	}
}

// tuneRedirectHandler hands /tune to the static tuning page.
func tuneRedirectHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/tune.html", http.StatusFound)
	}
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
