package tests

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"magitrickle"
	"magitrickle/api/v1"
	"magitrickle/api/v1/types"
	"magitrickle/models"
	"magitrickle/utils/intID"
)

func TestGroupEditsKeepSnapshots(t *testing.T) {
	a := &magitrickle.App{}
	id, ruleID := intID.ID{1}, intID.ID{2}
	old := &models.Rule{ID: ruleID, Type: models.RuleTypeDomain, Rule: "old.example", Enable: true}
	group := &models.Group{ID: id, Name: "old", Enable: true, Rules: []*models.Rule{old}}
	if err := a.AddGroup(group); err != nil {
		t.Fatal(err)
	}
	if !old.IsMatch("old.example") {
		t.Fatal("initial domain does not match")
	}
	router := v1.NewRouter(a)
	request := func(path string, payload any) *httptest.ResponseRecorder {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, path, bytes.NewReader(data)))
		return w
	}
	w := request("/groups/"+id.String()+"/rules/"+ruleID.String(), types.RuleReq{Type: models.RuleTypeDomain, Rule: "new.example", Enable: true})
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	updated := a.UserGroups()[0].Model().Rules[0]
	if !updated.IsMatch("new.example") || updated.IsMatch("old.example") {
		t.Fatal("edited domain did not replace the old rule")
	}
	if group.Rules[0] != old || !old.IsMatch("old.example") {
		t.Fatal("published model mutated")
	}
	wrong := intID.ID{9}
	w = request("/groups/"+id.String(), types.GroupReq{ID: &wrong, Name: "bad"})
	if w.Code != http.StatusBadRequest || a.UserGroups()[0].Model().Name != "old" {
		t.Fatal("invalid edit changed existing group")
	}
	w = request("/groups", types.GroupsReq{Groups: &[]types.GroupReq{{ID: &id, Name: "first"}, {ID: &id, Name: "duplicate"}}})
	if w.Code != http.StatusBadRequest || len(a.UserGroups()) != 1 || a.UserGroups()[0].Model().Name != "old" {
		t.Fatal("invalid bulk edit replaced groups")
	}
}

func TestConcurrentGroupAPIAndDNSMatching(t *testing.T) {
	a := &magitrickle.App{}
	id, ruleID := intID.ID{1}, intID.ID{2}
	if err := a.AddGroup(&models.Group{ID: id, Rules: []*models.Rule{{ID: ruleID, Type: models.RuleTypeDomain, Rule: "example.com", Enable: true}}}); err != nil {
		t.Fatal(err)
	}
	router := v1.NewRouter(a)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				data := []byte(`{"type":"domain","rule":"example.com","enable":true}`)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/groups/"+id.String()+"/rules/"+ruleID.String(), bytes.NewReader(data)))
				if w.Code != http.StatusOK && w.Code != http.StatusConflict {
					t.Errorf("edit: %d %s", w.Code, w.Body.String())
					return
				}
				for _, group := range a.UserGroups() {
					for _, rule := range group.Model().Rules {
						rule.IsMatch("example.com")
					}
				}
			}
		}()
	}
	wg.Wait()
}

type pausedRuleBody struct {
	io.Reader
	entered chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *pausedRuleBody) Read(p []byte) (int, error) {
	b.once.Do(func() { b.entered <- struct{}{}; <-b.release })
	return b.Reader.Read(p)
}

func TestConcurrentRuleCreationRejectsStaleSnapshot(t *testing.T) {
	a := &magitrickle.App{}
	id := intID.ID{1}
	if err := a.AddGroup(&models.Group{ID: id, Enable: true}); err != nil {
		t.Fatal(err)
	}
	router := v1.NewRouter(a)
	path := "/groups/" + id.String() + "/rules"
	payloads := [][]byte{
		[]byte(`{"type":"domain","rule":"one.example","enable":true}`),
		[]byte(`{"type":"domain","rule":"two.example","enable":true}`),
	}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	responses := make(chan struct{ index, code int }, 2)
	for i, payload := range payloads {
		go func() {
			body := &pausedRuleBody{Reader: bytes.NewReader(payload), entered: entered, release: release}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, body))
			responses <- struct{ index, code int }{i, w.Code}
		}()
	}
	for range payloads {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("requests did not capture their initial snapshots")
		}
	}
	unblock()
	ok, conflict := 0, -1
	for range payloads {
		select {
		case response := <-responses:
			switch response.code {
			case http.StatusOK:
				ok++
			case http.StatusConflict:
				conflict = response.index
			default:
				t.Fatalf("unexpected status %d", response.code)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent edits did not complete")
		}
	}
	if ok != 1 || conflict < 0 || len(a.UserGroups()[0].Model().Rules) != 1 {
		t.Fatal("a concurrent edit was silently overwritten")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payloads[conflict])))
	if w.Code != http.StatusOK || len(a.UserGroups()[0].Model().Rules) != 2 {
		t.Fatalf("retry did not preserve both rules: %d %s", w.Code, w.Body.String())
	}
}
