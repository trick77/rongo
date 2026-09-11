package timeline

import "testing"

func TestRecorder_detailLandsOnTheLatestStepOfThatName(t *testing.T) {
	r := New()
	r.Record("searching")
	r.Record("gathering")
	r.Detail("searching", map[string]any{"hits": 20})
	// A detail for a step never announced describes nothing the reader saw.
	r.Detail("routing", map[string]any{"rung": "x"})
	// An empty detail is no detail.
	r.Detail("gathering", nil)
	tr := r.Close()
	if tr.Steps[0].Detail["hits"] != 20 {
		t.Errorf("searching detail = %v, want hits 20", tr.Steps[0].Detail)
	}
	if tr.Steps[1].Detail != nil {
		t.Errorf("gathering got a detail from nowhere: %v", tr.Steps[1].Detail)
	}
	if len(tr.Steps) != 2 {
		t.Errorf("a detail invented a step: %+v", tr.Steps)
	}
}
