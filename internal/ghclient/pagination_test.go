package ghclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// Paginate walks rel="next" until it disappears, accumulating every
// page into one slice in order.
func TestPaginate_WalksLinkHeader(t *testing.T) {
	type item struct {
		ID int `json:"id"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		switch page {
		case "1":
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/x?page=2&per_page=100>; rel="next"`, "")) // relative
			_ = json.NewEncoder(w).Encode([]item{{ID: 1}, {ID: 2}})
		case "2":
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/x?page=3&per_page=100>; rel="next"`, ""))
			_ = json.NewEncoder(w).Encode([]item{{ID: 3}})
		case "3":
			// no Link header → walk terminates
			_ = json.NewEncoder(w).Encode([]item{{ID: 4}, {ID: 5}})
		default:
			http.Error(w, "unexpected page "+page, 500)
		}
	}))
	defer srv.Close()

	rl := NewRateLimiter(discard(), Options{Concurrency: 4})
	defer rl.Stop()
	c := New(Config{APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: discard()})

	got, err := Paginate[item](context.Background(), c, PriorityCronReconcile, "o", "/repos/o/r/x")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d (%v)", len(got), len(want), got)
	}
	for i, v := range got {
		if v.ID != want[i] {
			t.Fatalf("item[%d].id = %d, want %d", i, v.ID, want[i])
		}
	}
}

// per_page=100 is appended automatically when not already present, but
// preserved when the caller has already set it.
func TestPaginate_PerPageDefaulted(t *testing.T) {
	var perPage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		perPage = r.URL.Query().Get("per_page")
		_ = json.NewEncoder(w).Encode([]struct{}{})
	}))
	defer srv.Close()

	rl := NewRateLimiter(discard(), Options{Concurrency: 1})
	defer rl.Stop()
	c := New(Config{APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: discard()})

	if _, err := Paginate[struct{}](context.Background(), c, PriorityCronReconcile, "o", "/x"); err != nil {
		t.Fatal(err)
	}
	if perPage != "100" {
		t.Fatalf("per_page = %q, want 100", perPage)
	}

	if _, err := Paginate[struct{}](context.Background(), c, PriorityCronReconcile, "o", "/x?per_page=25"); err != nil {
		t.Fatal(err)
	}
	if perPage != "25" {
		t.Fatalf("per_page = %q, want 25 (caller-supplied)", perPage)
	}
}

// parseNextLink returns the URL inside the rel="next" angle brackets
// and tolerates absent/missing links and other rels.
func TestParseNextLink_Variants(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=10>; rel="last"`,
			"https://api.github.com/x?page=2"},
		{`<https://api.github.com/x?page=10>; rel="last"`, ""},
		{"", ""},
		{`<https://api.github.com/x?page=2>; rel="next"`, "https://api.github.com/x?page=2"},
	}
	for i, tc := range cases {
		if got := parseNextLink(tc.in); got != tc.want {
			t.Errorf("case %d: parseNextLink(%q) = %q, want %q", i, tc.in, got, tc.want)
		}
	}
}

// Pagination handles a deep walk without losing items — proves we
// don't drop pages when the cursor crosses many round trips.
func TestPaginate_DeepWalk(t *testing.T) {
	const totalPages = 7
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		if page < totalPages {
			w.Header().Set("Link",
				fmt.Sprintf(`</p?page=%d&per_page=100>; rel="next"`, page+1))
		}
		_ = json.NewEncoder(w).Encode([]int{page})
	}))
	defer srv.Close()

	rl := NewRateLimiter(discard(), Options{Concurrency: 1})
	defer rl.Stop()
	c := New(Config{APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: discard()})

	got, err := Paginate[int](context.Background(), c, PriorityCronReconcile, "o", "/p")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != totalPages {
		t.Fatalf("got %d pages, want %d (%v)", len(got), totalPages, got)
	}
	for i, v := range got {
		if v != i+1 {
			t.Fatalf("page[%d] = %d, want %d", i, v, i+1)
		}
	}
}
