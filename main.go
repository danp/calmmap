package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/mazznoer/colorgrad"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geo"
	"github.com/paulmach/orb/geojson"
	"github.com/peterbourgon/ff/v3/ffcli"
	"github.com/twpayne/go-kml"
	_ "modernc.org/sqlite"
)

func main() {
	var (
		rootFlagSet  = flag.NewFlagSet("calmmap", flag.ExitOnError)
		databaseFile = rootFlagSet.String("database-file", "data.db", "database filename")

		buildDBFlagSet     = flag.NewFlagSet("calmmap builddb", flag.ExitOnError)
		centerlinesKMLFile = buildDBFlagSet.String("centerlines-kml-file", "street_centrelines.kml", "street centerlines KML file")
		calmingRequestFile = buildDBFlagSet.String("calming-requests-file", "street-calming-ranked-2020-11.tsv", "calming requests TSV file")
	)

	withSqliteStore := func(inner func(context.Context, *sqliteStore, []string) error) func(context.Context, []string) error {
		return (func(ctx context.Context, args []string) error {
			db, err := sql.Open("sqlite", *databaseFile)
			if err != nil {
				return err
			}
			defer db.Close()

			st := &sqliteStore{db: db}
			return inner(ctx, st, args)
		})
	}

	withStore := func(inner func(context.Context, store, []string) error) func(context.Context, []string) error {
		return withSqliteStore(func(ctx context.Context, st *sqliteStore, args []string) error {
			return inner(ctx, st, args)
		})
	}

	cmdBuildDB := &ffcli.Command{
		Name:      "builddb",
		ShortHelp: "build database from centreline and request data",
		Exec: withSqliteStore(func(_ context.Context, st *sqliteStore, _ []string) error {
			if err := st.init(); err != nil {
				return err
			}

			kf, err := os.Open(*centerlinesKMLFile)
			if err != nil {
				return err
			}
			defer kf.Close()

			rf, err := os.Open(*calmingRequestFile)
			if err != nil {
				return err
			}
			defer rf.Close()

			if err := loadKMLSegments(st, kf); err != nil {
				return err
			}

			return loadTSVRequests(st, rf)
		}),
	}

	cmdFixup := &ffcli.Command{
		Name:      "fixup",
		ShortHelp: "run interactive validation tool",
		Exec:      withStore(fixup),
	}

	cmdRouteViz := &ffcli.Command{
		Name:      "routeviz",
		ShortHelp: "generate dot graph for a route id",
		Exec:      withStore(routeViz),
	}

	cmdExport := &ffcli.Command{
		Name:      "export",
		ShortHelp: "export map KML for requests",
		Exec:      withStore(export),
	}

	root := &ffcli.Command{
		ShortUsage:  "calmmap [flags] <subcommand>",
		FlagSet:     rootFlagSet,
		Subcommands: []*ffcli.Command{cmdBuildDB, cmdFixup, cmdRouteViz, cmdExport},
		Exec: func(context.Context, []string) error {
			return flag.ErrHelp
		},
	}

	if err := root.ParseAndRun(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type store interface {
	requests() ([]request, error)
	filterSegments(segmentFilter) ([]segment, error)
	routeLinks(routeID int) (map[int][]int, error)
	route([]segment, []segment) ([]segment, error)
}

func routeViz(_ context.Context, st store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("need route id")
	}

	routeID, err := strconv.Atoi(args[0])
	if err != nil {
		return err
	}

	segs, err := st.filterSegments(segmentFilter{routeIDs: []int{routeID}})
	if err != nil {
		return err
	}

	if len(segs) == 0 {
		return fmt.Errorf("no segments found for route %d", routeID)
	}

	links, err := st.routeLinks(routeID)
	if err != nil {
		return err
	}

	fmt.Println("digraph {")
	fmt.Printf("  label=%q\n", segs[0].name)
	for _, seg := range segs {
		fmt.Printf("  n%d [label=%q];\n", seg.id, fmt.Sprintf("%s to %s", seg.from, seg.to))
	}
	for id, nexts := range links {
		for _, next := range nexts {
			fmt.Printf("  n%d -> n%d;\n", id, next)
		}
	}
	fmt.Println("}")

	return nil
}

func export(_ context.Context, st store, args []string) error {
	reqs, err := st.requests()
	if err != nil {
		return err
	}

	// https://play.golang.org/p/hFSq1nYn-eX
	grad, err := colorgrad.NewGradient().HtmlColors("#aa0026", "darkorange", "#8d8d8d").Build()
	if err != nil {
		return err
	}
	colors := grad.Colors(20)

	var placemarks []kml.Element

	for _, req := range reqs {
		hand := newDefaultRequestHandler(st, req)

		res, err := hand.handle()
		if err != nil {
			log.Println(req, "error:", err)
			continue
		}

		var lineStrings []kml.Element
		for _, seg := range res.routeSegments {
			coords := make([]kml.Coordinate, 0, len(seg.lineString))
			for _, lsp := range seg.lineString {
				coords = append(coords, kml.Coordinate{Lon: lsp.Lon(), Lat: lsp.Lat()})
			}
			lineStrings = append(lineStrings, kml.LineString(kml.Coordinates(coords...)))
		}

		colorGroup := req.rank / (len(reqs) / len(colors))
		if colorGroup >= len(colors) {
			colorGroup = len(colors) - 1
		}

		placemarks = append(placemarks, kml.Placemark(
			kml.Name(req.String()),
			kml.StyleURL(fmt.Sprintf("#line-group-%d", colorGroup)),
			kml.MultiGeometry(lineStrings...),
		))
	}

	folder := kml.Folder(kml.Name("Calming Requests, ranked and coloured by rank"))
	folder.Add(placemarks...)

	doc := kml.Document()
	for i, col := range colors {
		doc.Add(kml.SharedStyle(fmt.Sprintf("line-group-%d", i), kml.LineStyle(kml.Width(4), kml.Color(col))))
	}
	doc.Add(folder)
	k := kml.KML(doc)
	return k.WriteIndent(os.Stdout, "", "  ")
}

type requestResult struct {
	startSegments []segment
	endSegments   []segment
	routeSegments []segment
}

type requestHandler struct {
	req request

	startHandler func(processingRequest) ([]segment, error)
	endHandler   func(processingRequest) ([]segment, error)
	routeHandler func(processingRequest) ([]segment, error)
}

func newDefaultRequestHandler(st store, req request) requestHandler {
	return requestHandler{
		req:          req,
		startHandler: overrideDiscovery("start", st, startDiscovery(st)),
		endHandler:   overrideDiscovery("end", st, endDiscovery(st)),
		routeHandler: overrideDiscovery("route", st, routeDiscovery(st)),
	}
}

type processingRequest struct {
	req           request
	startSegments []segment
	endSegments   []segment
}

type requestAttempt struct {
	startSegments []segment
	startErr      error

	endSegments []segment
	endErr      error

	routeSegments []segment
	routeErr      error
}

func (s requestHandler) handleAttempt() requestAttempt {
	att := requestAttempt{}

	preq := processingRequest{
		req: s.req,
	}

	att.startSegments, att.startErr = s.startHandler(preq)
	if len(att.startSegments) == 0 {
		att.startErr = fmt.Errorf("no start segments found")
		att.endErr = fmt.Errorf("no start segments found")
		att.routeErr = fmt.Errorf("no start segments found")
		return att
	}
	preq.startSegments = att.startSegments

	att.endSegments, att.endErr = s.endHandler(preq)
	if len(att.endSegments) == 0 {
		att.endErr = fmt.Errorf("no end segments found")
		att.routeErr = fmt.Errorf("no end segments found")
		return att
	}
	preq.endSegments = att.endSegments

	att.routeSegments, att.routeErr = s.routeHandler(preq)
	return att
}

func (s requestHandler) handle() (requestResult, error) {
	att := s.handleAttempt()

	for _, err := range []error{att.startErr, att.endErr, att.routeErr} {
		if err != nil {
			return requestResult{}, err
		}
	}

	return requestResult{
		startSegments: att.startSegments,
		endSegments:   att.endSegments,
		routeSegments: att.routeSegments,
	}, nil
}

func overrideDiscovery(when string, st store, next func(preq processingRequest) ([]segment, error)) func(preq processingRequest) ([]segment, error) {
	return func(preq processingRequest) ([]segment, error) {
		f, err := os.Open(fmt.Sprintf("overrides/%d.%s", preq.req.rank, when))
		if os.IsNotExist(err) {
			return next(preq)
		}
		if err != nil {
			return nil, err
		}
		defer f.Close()

		var ids []int
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			id, err := strconv.Atoi(sc.Text())
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		if sc.Err() != nil {
			return nil, sc.Err()
		}
		return st.filterSegments(segmentFilter{ids: ids})
	}
}

func startDiscovery(st store) func(preq processingRequest) ([]segment, error) {
	return func(preq processingRequest) ([]segment, error) {
		filter := segmentFilter{fullNames: []string{strings.ReplaceAll(preq.req.streetName, "'", "")}}
		if preq.req.from != "" {
			dqf := strings.ReplaceAll(preq.req.from, "'", "")
			filter.endStreets = []string{dqf}
		}

		segs, err := st.filterSegments(filter)
		if err != nil {
			return nil, err
		}

		routeIDs := make(map[int]bool)
		for _, seg := range segs {
			routeIDs[seg.routeID] = true
		}
		if len(routeIDs) > 1 {
			return nil, fmt.Errorf("discovered segments with %d different route IDs", len(routeIDs))
		}

		return segs, nil
	}
}

func endDiscovery(st store) func(preq processingRequest) ([]segment, error) {
	return func(preq processingRequest) ([]segment, error) {
		filter := segmentFilter{routeIDs: []int{preq.startSegments[0].routeID}}
		if preq.req.to != "" {
			dqt := strings.ReplaceAll(preq.req.to, "'", "")
			filter.endStreets = []string{dqt}
		}

		segs, err := st.filterSegments(filter)
		if err != nil {
			return nil, err
		}

		return segs, nil
	}
}

func routeDiscovery(st store) func(preq processingRequest) ([]segment, error) {
	return func(preq processingRequest) ([]segment, error) {
		// For entire streets, return all segments on the route.
		if preq.req.from == "" && preq.req.to == "" {
			return st.filterSegments(segmentFilter{routeIDs: []int{preq.startSegments[0].routeID}})
		}

		route, err := st.route(preq.startSegments, preq.endSegments)
		if err != nil {
			return nil, err
		}

		// If there's a start, trim the start of the path so it only
		// begins with one start segment.
		if preq.req.from != "" {
			startIDs := make([]int, len(preq.startSegments))
			for _, seg := range preq.startSegments {
				startIDs = append(startIDs, seg.id)
			}
			for len(route) > 2 && contains(startIDs, route[1].id) {
				route = route[1:]
			}
		}

		// For "from X to end" requests or when "from X to X", find the longest route (by segment count).
		//
		// An example of the latter is "Summit Cres from High Timber Dr to High Timber Dr"
		if preq.req.to == "" || preq.req.to == preq.req.from {
			path := route
			for _, end := range preq.endSegments {
				c, err := st.route([]segment{route[0]}, []segment{end})
				if err != nil {
					return nil, err
				}
				if len(c) > len(path) {
					path = c
				}
			}
			route = path
		}

		return route, nil
	}
}

type request struct {
	streetName string
	from, to   string
	district   string
	rank       int
}

func (r request) String() string {
	out := strconv.Itoa(r.rank) + " " + r.streetName + " "
	if r.from == "" && r.to == "" {
		out += "(all)"
	} else {
		out += "from " + r.from
		if r.to != "" {
			out += " to " + r.to
		}
	}
	return out
}

func (s sqliteStore) requests() ([]request, error) {
	rows, err := s.db.Query("select street_name, start, end, district, rank from requests order by rank")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reqs []request
	for rows.Next() {
		var req request
		var start, end sql.NullString
		if err := rows.Scan(&req.streetName, &start, &end, &req.district, &req.rank); err != nil {
			return nil, err
		}
		req.from = start.String
		req.to = end.String
		reqs = append(reqs, req)
	}

	return reqs, rows.Err()
}

type segment struct {
	id        int
	name      string
	from, to  string
	direction string
	routeID   int

	lineString orb.LineString
	firstPoint orb.Point
	lastPoint  orb.Point

	streetName  string
	streetType  string
	streetClass string
}

func (s segment) String() string {
	return fmt.Sprintf("%d %s from %s to %s", s.id, s.name, s.from, s.to)
}

type sqliteStore struct {
	db *sql.DB
}

// route finds a route between any of the fromSegments to any of the toSegments.
func (s sqliteStore) route(fromSegments []segment, toSegments []segment) ([]segment, error) {
	if len(fromSegments) == 0 || len(toSegments) == 0 {
		return nil, fmt.Errorf("empty fromSegments or empty toSegments")
	}

	rows, err := s.db.Query("select id, next_id from segment_links where route_id=(select route_id from segments where id=?)", fromSegments[0].id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// First fromSegments is always in the graph, even if it has no edges.
	graph := map[int][]int{
		fromSegments[0].id: nil,
	}
	for rows.Next() {
		var id, nextID int
		if err := rows.Scan(&id, &nextID); err != nil {
			return nil, err
		}
		graph[id] = append(graph[id], nextID)
		if _, ok := graph[nextID]; !ok {
			graph[nextID] = nil
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, seg := range toSegments {
		if _, ok := graph[seg.id]; !ok {
			return nil, fmt.Errorf("to segment %d not found in route graph", seg.id)
		}
	}

	toIDs := make([]int, 0, len(toSegments))
	for _, seg := range toSegments {
		toIDs = append(toIDs, seg.id)
	}

	q := [][]int{{fromSegments[0].id}}
	var path []int
	for len(q) > 0 {
		p := q[0]
		q = q[1:]
		lid := p[len(p)-1]

		if contains(toIDs, lid) {
			path = p
			break
		}

		for _, nid := range graph[lid] {
			if contains(p, nid) {
				continue
			}
			newp := make([]int, len(p))
			copy(newp, p)
			newp = append(newp, nid)
			q = append(q, newp)
		}
	}

	if path == nil {
		return nil, fmt.Errorf("could not find path")
	}

	segs, err := s.filterSegments(segmentFilter{ids: path})
	if err != nil {
		return nil, err
	}
	segsByID := make(map[int]segment)
	for _, seg := range segs {
		segsByID[seg.id] = seg
	}
	for i, id := range path {
		segs[i] = segsByID[id]
	}
	return segs, nil
}

func (s sqliteStore) routeLinks(routeID int) (map[int][]int, error) {
	links := make(map[int][]int)

	rows, err := s.db.Query("select id, next_id from segment_links where route_id=?", routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id, nextID int
		if err := rows.Scan(&id, &nextID); err != nil {
			return nil, err
		}
		links[id] = append(links[id], nextID)
	}

	return links, rows.Err()
}

// Uses approach described in https://www.gobeyond.dev/real-world-sql-part-one/ but with
// slices instead of pointers to ints/etc.
type segmentFilter struct {
	ids        []int
	fullNames  []string
	routeIDs   []int
	endStreets []string
}

func (s sqliteStore) filterSegments(filter segmentFilter) ([]segment, error) {
	where, args := []string{"1 = 1"}, []interface{}{}

	if len(filter.ids) > 0 {
		var idw []string
		for _, id := range filter.ids {
			idw = append(idw, "id = ?")
			args = append(args, id)
		}
		where = append(where, "("+strings.Join(idw, " or ")+")")
	}

	if len(filter.fullNames) > 0 {
		var fnw []string
		for _, fn := range filter.fullNames {
			fnw = append(fnw, "full_name = upper(?)")
			args = append(args, fn)
		}
		where = append(where, "("+strings.Join(fnw, " or ")+")")
	}

	if len(filter.routeIDs) > 0 {
		var idw []string
		for _, id := range filter.routeIDs {
			idw = append(idw, "route_id = ?")
			args = append(args, id)
		}
		where = append(where, "("+strings.Join(idw, " or ")+")")
	}

	if len(filter.endStreets) > 0 {
		var esw []string
		for _, es := range filter.endStreets {
			esw = append(esw, "upper(?) in (from_str, to_str)")
			args = append(args, es)
		}
		where = append(where, "("+strings.Join(esw, " or ")+")")
	}

	q := "select id, full_name, from_str, to_str, route_id, direction, line_string, first_point, last_point, str_name, str_type, st_class from segments where "
	q += strings.Join(where, " and ")

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var segs []segment
	for rows.Next() {
		var seg segment
		var (
			lsb []byte
			fpb []byte
			lpb []byte
		)
		if err := rows.Scan(&seg.id, &seg.name, &seg.from, &seg.to, &seg.routeID, &seg.direction, &lsb, &fpb, &lpb, &seg.streetName, &seg.streetType, &seg.streetClass); err != nil {
			return nil, err
		}

		var jls geojson.LineString
		if err := json.Unmarshal(lsb, &jls); err != nil {
			return nil, err
		}
		seg.lineString = orb.LineString(jls)

		var fpt geojson.Point
		if err := json.Unmarshal(fpb, &fpt); err != nil {
			return nil, err
		}
		seg.firstPoint = orb.Point(fpt)

		var lpt geojson.Point
		if err := json.Unmarshal(lpb, &lpt); err != nil {
			return nil, err
		}
		seg.lastPoint = orb.Point(lpt)

		segs = append(segs, seg)
	}

	return segs, rows.Err()
}

func (s sqliteStore) init() error {
	for _, q := range []string{
		"create table segments (id integer primary key, str_name text, str_type text, st_class, full_name text, from_str text, to_str text, route_id integer, direction text, line_string json, first_point json, last_point json)",
		"create table segment_links (id integer, route_id integer, next_id integer)",
		"create table requests (id integer primary key, street_name text not null, start text, end text, district text, rank integer)",
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}

	return nil
}

func (s sqliteStore) loadSegments(segments []segment) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	routeSegments := make(map[int][]segment)

	for _, seg := range segments {
		routeSegments[seg.routeID] = append(routeSegments[seg.routeID], seg)

		lsb, err := json.Marshal(geojson.LineString(seg.lineString))
		if err != nil {
			return err
		}

		fpb, err := json.Marshal(geojson.Point(seg.firstPoint))
		if err != nil {
			return err
		}

		lpb, err := json.Marshal(geojson.Point(seg.lastPoint))
		if err != nil {
			return err
		}

		if _, err := tx.Exec("insert into segments (id, str_name, str_type, st_class, full_name, from_str, to_str, route_id, direction, line_string, first_point, last_point) values (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)",
			seg.id,
			seg.streetName,
			seg.streetType,
			seg.streetClass,
			seg.name,
			seg.from,
			seg.to,
			seg.routeID,
			seg.direction,
			lsb,
			fpb,
			lpb,
		); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	isClose := func(a, b orb.Point) bool {
		return geo.Distance(a, b) < 1.0
	}

	matches := func(segs []segment, cur segment) ([]segment, error) {
		var out []segment
		for _, next := range segs {
			if cur.id == next.id {
				continue
			}

			switch cur.direction + " " + next.direction {
			case "BOTH BOTH":
				if isClose(cur.firstPoint, next.firstPoint) || isClose(cur.firstPoint, next.lastPoint) || isClose(cur.lastPoint, next.firstPoint) || isClose(cur.lastPoint, next.lastPoint) {
					out = append(out, next)
				}
			case "BOTH FOTD":
				if isClose(next.firstPoint, cur.firstPoint) || isClose(next.firstPoint, cur.lastPoint) {
					out = append(out, next)
				}
			case "BOTH FDTO":
			case "FOTD FOTD":
				if isClose(next.firstPoint, cur.lastPoint) {
					out = append(out, next)
				}
			case "FOTD BOTH":
				if isClose(cur.lastPoint, next.firstPoint) || isClose(cur.lastPoint, next.lastPoint) {
					out = append(out, next)
				}
			case "FDTO BOTH":
			default:
				return nil, fmt.Errorf("unknown direction pair: %s and %s, %s / %s", cur.direction, next.direction, cur, next)
			}
		}
		return out, nil
	}

	tx, err = s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, routeSegs := range routeSegments {
		for _, seg := range routeSegs {
			nextSegs, err := matches(routeSegs, seg)
			if err != nil {
				return err
			}
			for _, next := range nextSegs {
				if _, err := tx.Exec("insert into segment_links (id, route_id, next_id) values (?, ?, ?)",
					seg.id, seg.routeID, next.id,
				); err != nil {
					return err
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	for _, q := range []string{
		"create index segment_links_id on segment_links(id)",
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}

	return nil
}

func (s sqliteStore) loadRequests(reqs []request) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, req := range reqs {
		var start, end sql.NullString
		if req.from != "" {
			start.String = req.from
			start.Valid = true
		}
		if req.to != "" {
			end.String = req.to
			end.Valid = true
		}

		if _, err := tx.Exec("insert into requests (street_name, start, end, district, rank) values (?, ?, ?, ?, ?)",
			req.streetName, start, end, req.district, req.rank,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func loadKMLSegments(st *sqliteStore, kmlReader io.Reader) error {
	var d document
	if err := xml.NewDecoder(kmlReader).Decode(&d); err != nil {
		return err
	}

	segments := make([]segment, 0, len(d.Document.Folder.Placemark))
	for _, p := range d.Document.Folder.Placemark {
		var ls orb.LineString
		for _, lsf := range strings.Fields(p.MultiGeometry.LineString) {
			var pt orb.Point
			if _, err := fmt.Sscanf(lsf, "%f,%f", &pt[0], &pt[1]); err != nil {
				return err
			}
			ls = append(ls, pt)
		}

		data := p.data()

		id, err := strconv.Atoi(data["FDMID"])
		if err != nil {
			return err
		}
		routeID, err := strconv.Atoi(data["ROUTE_ID"])
		if err != nil {
			return err
		}

		seg := segment{
			id:          id,
			streetName:  data["STR_NAME"],
			streetType:  data["STR_TYPE"],
			streetClass: data["ST_CLASS"],
			name:        data["FULL_NAME"],
			from:        data["FROM_STR"],
			to:          data["TO_STR"],
			routeID:     routeID,
			direction:   data["STR_DIR"],
			lineString:  ls,
			firstPoint:  ls[0],
			lastPoint:   ls[len(ls)-1],
		}
		segments = append(segments, seg)
	}

	return st.loadSegments(segments)
}

func loadTSVRequests(st *sqliteStore, requestReader io.Reader) error {
	var reqs []request

	sc := bufio.NewScanner(requestReader)
	first := true
	for sc.Scan() {
		if first {
			first = false // header
			continue
		}
		line := sc.Text()
		fields := strings.Split(line, "\t")

		var start, end string
		if fields[2] != "" && strings.ToLower(fields[2]) != "all" {
			start = fields[2]
		}
		if fields[3] != "" && strings.ToLower(fields[3]) != "end" {
			end = fields[3]
		}
		rank, err := strconv.Atoi(fields[0])
		if err != nil {
			return err
		}

		req := request{
			streetName: fields[1],
			from:       start,
			to:         end,
			rank:       rank,
			district:   fields[4],
		}
		reqs = append(reqs, req)
	}

	if sc.Err() != nil {
		return sc.Err()
	}

	return st.loadRequests(reqs)
}

type document struct {
	Document struct {
		Folder folder
	}
}

type folder struct {
	Placemark []placemark
}

type placemark struct {
	ExtendedData struct {
		SchemaData struct {
			SimpleData []simpleData
		}
	}

	MultiGeometry struct {
		LineString string `xml:">coordinates"`
	}
}

func (p placemark) data() map[string]string {
	out := make(map[string]string)
	for _, sd := range p.ExtendedData.SchemaData.SimpleData {
		out[sd.Name] = sd.Data
	}
	return out
}

type simpleData struct {
	Name string `xml:"name,attr"`
	Data string `xml:",innerxml"`
}

func contains(x []int, y int) bool {
	for _, z := range x {
		if z == y {
			return true
		}
	}
	return false
}
