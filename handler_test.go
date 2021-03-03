package main

import (
	"database/sql"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/paulmach/orb"
)

func TestStartDiscovery(t *testing.T) {
	var (
		s1  = segment{id: 1, name: "TEST LN", from: "A ST", to: "B ST", routeID: 1, direction: "BOTH"}
		s2  = segment{id: 2, name: "TEST LN", from: "B ST", to: "C ST", routeID: 1, direction: "BOTH"}
		qs1 = segment{id: 5, name: "TESTN LN", from: "AN ST", to: "B ST", routeID: 1, direction: "BOTH"}
		irr = segment{id: 10, name: "IRRELEVANT PL", from: "A ST", to: "B ST", routeID: 2, direction: "BOTH"}
	)

	cases := []struct {
		name string
		in   []segment
		req  request
		want []segment
	}{
		{
			name: "Easy",
			in:   []segment{s1, irr},
			req:  request{streetName: "Test Ln", from: "A St", to: "Nowhere Crs"},
			want: []segment{s1},
		},
		{
			name: "NoFrom",
			in:   []segment{s1},
			req:  request{streetName: "Test Ln"},
			want: []segment{s1},
		},
		{
			name: "Multiple",
			in:   []segment{s1, s2},
			req:  request{streetName: "Test Ln", from: "B St", to: "Nowhere Crs"},
			want: []segment{s1, s2},
		},
		{
			name: "QuotesStreetAndFrom",
			in:   []segment{qs1},
			req:  request{streetName: "Test'n Ln", from: "A'n St", to: "Nowhere Crs"},
			want: []segment{qs1},
		},
		{
			name: "NoMatch",
			in:   []segment{s1},
			req:  request{streetName: "Test Ln", from: "Nonexistent St", to: "Nowhere Crs"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file::memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			st := &sqliteStore{db: db}
			if err := st.init(); err != nil {
				t.Fatal(err)
			}

			if err := st.loadSegments(tc.in); err != nil {
				t.Fatal(err)
			}

			sd := startDiscovery(st)

			preq := processingRequest{
				req: tc.req,
			}

			segs, err := sd(preq)
			if err != nil {
				t.Fatal(err)
			}

			if d := cmp.Diff(tc.want, segs, cmp.AllowUnexported(segment{})); d != "" {
				t.Errorf("discovered segment mismatch (-want +got):\n%s", d)
			}
		})
	}
}

func TestStartDiscoveryError(t *testing.T) {
	var (
		a1 = segment{id: 1, name: "TEST LN", from: "A ST", to: "B ST", routeID: 1, direction: "BOTH"}
		a2 = segment{id: 2, name: "TEST LN", from: "A ST", to: "B ST", routeID: 2, direction: "BOTH"}
	)

	cases := []struct {
		name string
		in   []segment
		req  request
	}{
		{
			name: "RouteConflict",
			in:   []segment{a1, a2},
			req:  request{streetName: "Test Ln", from: "A St", to: "Nowhere Crs"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file::memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			st := &sqliteStore{db: db}
			if err := st.init(); err != nil {
				t.Fatal(err)
			}

			if err := st.loadSegments(tc.in); err != nil {
				t.Fatal(err)
			}

			sd := startDiscovery(st)

			preq := processingRequest{
				req: tc.req,
			}

			_, err = sd(preq)
			if err == nil {
				t.Fatal("wanted error")
			}
		})
	}
}

func TestEndDiscovery(t *testing.T) {
	var (
		s1  = segment{id: 1, name: "TEST LN", from: "A ST", to: "B ST", routeID: 1, direction: "BOTH"}
		s2  = segment{id: 2, name: "TEST LN", from: "B ST", to: "C ST", routeID: 1, direction: "BOTH"}
		s3  = segment{id: 3, name: "TEST LN", from: "C ST", to: "D ST", routeID: 1, direction: "BOTH"}
		qs1 = segment{id: 1, name: "TEST LN", from: "A ST", to: "BN ST", routeID: 1, direction: "BOTH"}
		irr = segment{id: 10, name: "IRRELEVANT PL", from: "A ST", to: "B ST", routeID: 2, direction: "BOTH"}
	)

	cases := []struct {
		name  string
		in    []segment
		start []segment
		req   request
		want  []segment
	}{
		{
			name:  "Easy",
			in:    []segment{s2, s3, irr},
			start: []segment{s1},
			req:   request{streetName: "Test Ln", from: "A St", to: "D St"},
			want:  []segment{s3},
		},
		{
			name:  "NoMatch",
			in:    []segment{irr},
			start: []segment{s1},
			req:   request{streetName: "Test Ln", from: "Foo St", to: "Nowhere St"},
		},
		{
			name:  "Multiple",
			in:    []segment{s2, s3},
			start: []segment{s1},
			req:   request{streetName: "Test Ln", from: "A St", to: "C St"},
			want:  []segment{s2, s3},
		},
		{
			name:  "NoTo",
			in:    []segment{s1, s2, s3},
			start: []segment{s1},
			req:   request{streetName: "Test Ln", from: "B St"},
			want:  []segment{s1, s2, s3},
		},
		{
			name:  "QuotesTo",
			in:    []segment{qs1},
			start: []segment{qs1},
			req:   request{streetName: "Test Ln", from: "A St", to: "B'n St"},
			want:  []segment{qs1},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file::memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			st := &sqliteStore{db: db}
			if err := st.init(); err != nil {
				t.Fatal(err)
			}

			if err := st.loadSegments(tc.in); err != nil {
				t.Fatal(err)
			}

			preq := processingRequest{
				startSegments: tc.start,
				req:           tc.req,
			}

			ed := endDiscovery(st)

			segs, err := ed(preq)
			if err != nil {
				t.Fatal(err)
			}

			if d := cmp.Diff(tc.want, segs, cmp.AllowUnexported(segment{})); d != "" {
				t.Errorf("discovered segment mismatch (-want +got):\n%s", d)
			}
		})
	}
}

func TestRouteDiscovery(t *testing.T) {
	var (
		s1 = segment{id: 1, name: "TEST LN", from: "A ST", to: "B ST", routeID: 1, direction: "BOTH", lastPoint: orb.Point{0, 1}}
		s2 = segment{id: 2, name: "TEST LN", from: "B ST", to: "C ST", routeID: 1, direction: "BOTH", firstPoint: orb.Point{0, 1}, lastPoint: orb.Point{0, 2}}
		s3 = segment{id: 3, name: "TEST LN", from: "C ST", to: "D ST", routeID: 1, direction: "BOTH", firstPoint: orb.Point{0, 2}, lastPoint: orb.Point{0, 3}}
		s4 = segment{id: 4, name: "TEST LN", from: "D ST", to: "E ST", routeID: 1, direction: "BOTH", firstPoint: orb.Point{0, 3}, lastPoint: orb.Point{0, 4}}
		s5 = segment{id: 5, name: "TEST LN", from: "E ST", to: "F ST", routeID: 1, direction: "BOTH", firstPoint: orb.Point{0, 4}, lastPoint: orb.Point{0, 5}}

		j1 = segment{id: 1, name: "TEST LN", from: "A ST", to: "B ST", routeID: 1, direction: "BOTH", lastPoint: orb.Point{0, 1}}
		j2 = segment{id: 2, name: "TEST LN", from: "B ST", to: "A ST", routeID: 1, direction: "BOTH", firstPoint: orb.Point{0, 1}, lastPoint: orb.Point{0, 2}}

		irr = segment{id: 10, name: "IRRELEVANT PL", from: "A ST", to: "B ST", routeID: 2, direction: "BOTH"}
	)

	cases := []struct {
		name  string
		in    []segment
		start []segment
		end   []segment
		req   request
		want  []segment
	}{
		{
			name:  "Easy",
			in:    []segment{s1, s2, s3, s4, irr},
			start: []segment{s1},
			end:   []segment{s4},
			req: request{
				streetName: "Test Ln",
				from:       "A St",
				to:         "E St",
			},
			want: []segment{s1, s2, s3, s4},
		},
		{
			name:  "OnlyOneStartSegment",
			in:    []segment{s1, s2, s3, s4, irr},
			start: []segment{s1, s2},
			end:   []segment{s4},
			req:   request{streetName: "Test Ln", from: "A St", to: "E St"},
			want:  []segment{s2, s3, s4},
		},
		{
			name:  "WholeStreet",
			in:    []segment{s1, s2, s3, s4, irr},
			start: []segment{s1},
			end:   []segment{s4},
			req:   request{streetName: "Test Ln"},
			want:  []segment{s1, s2, s3, s4},
		},
		{
			name:  "ToEnd",
			in:    []segment{s1, s2, s3, s4, s5, irr},
			start: []segment{s2},
			end:   []segment{s3, s4, s5},
			req:   request{streetName: "Test Ln", from: "A St"},
			want:  []segment{s2, s3, s4, s5}, // longer than s2,s1
		},
		{
			name:  "PreferOtherWhenToFromMatch",
			in:    []segment{j1, j2},
			start: []segment{j1, j2},
			end:   []segment{j1, j2},
			req:   request{streetName: "Test Ln", from: "A St", to: "A St"},
			want:  []segment{j1, j2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file::memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			st := &sqliteStore{db: db}
			if err := st.init(); err != nil {
				t.Fatal(err)
			}

			if err := st.loadSegments(tc.in); err != nil {
				t.Fatal(err)
			}

			preq := processingRequest{
				startSegments: tc.start,
				endSegments:   tc.end,
				req:           tc.req,
			}

			rd := routeDiscovery(st)

			route, err := rd(preq)
			if err != nil {
				t.Fatal(err)
			}

			if d := cmp.Diff(tc.want, route, cmp.AllowUnexported(segment{})); d != "" {
				t.Errorf("discovered route mismatch (-want +got):\n%s", d)
			}
		})
	}
}
