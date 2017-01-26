package tests2

import (
    "context"
    "fmt"
    "math"
    "math/rand"
    "testing"
    "time"

    "github.com/pborman/uuid"

    "github.com/SoftwareDefinedBuildings/btrdb/bte"
    "gopkg.in/btrdb.v4"
)

/* Helper functions. */

func helperConnect(t *testing.T, ctx context.Context) *btrdb.BTrDB {
    db, err := btrdb.Connect(ctx, btrdb.EndpointsFromEnv()...)
    if err != nil {
		t.Fatalf("Unexpected connection error: %v", err)
	}
    return db
}

func helperCreateDefaultStream(t *testing.T, ctx context.Context, db *btrdb.BTrDB, tags map[string]string, ann []byte) *btrdb.Stream {
    uu := uuid.NewRandom()
    coll := helperGetCollection(uu)
    s := helperCreateStream(t, ctx, db, uu, coll, tags, ann)
    suu := s.UUID()
    if len(suu) != len(uu) {
        t.Fatal("Bad UUID")
    }
    for i, b := range suu {
        if b != uu[i] {
            t.Fatal("UUID of created stream does not match provided UUID")
        }
    }
    return s
}

func helperGetCollection(uu uuid.UUID) string {
    return fmt.Sprintf("test.%x", uu[:])
}

func helperCreateStream(t *testing.T, ctx context.Context, db *btrdb.BTrDB, uu uuid.UUID, coll string, tags map[string]string, ann []byte) *btrdb.Stream {
    stream, err := db.Create(ctx, uu, coll, tags, ann)
    if err != nil {
		t.Fatalf("create error %v", err)
	}
    return stream
}

func helperWaitAfterInsert() {
    time.Sleep(12 * time.Second)
}

func helperInsert(t *testing.T, ctx context.Context, s *btrdb.Stream, data []btrdb.RawPoint) {
    err := s.Insert(ctx, data)
    if err != nil {
        t.Fatalf("insert error %v", err)
    }
    helperWaitAfterInsert()
}

func helperInsertTV(t *testing.T, ctx context.Context, s *btrdb.Stream, times []int64, values []float64) {
    err := s.InsertTV(ctx, times, values)
    if err != nil {
        t.Fatalf("insert error %v", err)
    }
    helperWaitAfterInsert()
}

func helperRandomData(start int64, end int64, gap int64) []btrdb.RawPoint {
    numpts := (end - start) / gap
    pts := make([]btrdb.RawPoint, numpts)
    for i, _ := range pts {
        pts[i].Time = start + (int64(i) * gap)
        pts[i].Value = rand.NormFloat64()
    }
    return pts
}

func helperRandomDataCount(start int64, end int64, numpts int64) []btrdb.RawPoint {
    gap := (end - start) / numpts
    return helperRandomData(start, end, gap)
}

func helperRawQuery(t *testing.T, ctx context.Context, s *btrdb.Stream, start int64, end int64, version uint64) ([]btrdb.RawPoint, uint64) {
    rpc, verc, errc := s.RawValues(ctx, start, end, version)
    rv := make([]btrdb.RawPoint, 16)
    for rp := range rpc {
        rv = append(rv, rp)
    }
    ver := <-verc
    err := <-errc
    if err != nil {
        t.Fatalf("raw query error: %v", err)
    }

    return rv, ver
}

func helperWindowQuery(t *testing.T, ctx context.Context, s *btrdb.Stream, start int64, end int64, width uint64, depth uint8, version uint64) ([]btrdb.StatPoint, uint64) {
    spc, verc, errc := s.Windows(ctx, start, end, width, depth, version)
    rv := make([]btrdb.StatPoint, 16)
    for sp := range spc {
        rv = append(rv, sp)
    }
    ver := <-verc
    err := <-errc
    if err != nil {
        t.Fatalf("window query error: %v", err)
    }

    return rv, ver
}

const CANONICAL_END int64 = 1000000000000000000
const CANONICAL_START int64 = 100
const CANONICAL_COUNT int = 10000
const CANONICAL_FINAL int64 = CANONICAL_START + int64(CANONICAL_COUNT - 1) * ((CANONICAL_END - CANONICAL_START) / int64(CANONICAL_COUNT))
func helperCanonicalData() []btrdb.RawPoint {
    return helperRandomDataCount(CANONICAL_START, CANONICAL_END, int64(CANONICAL_COUNT))
}

const BTRDB_LOW int64 = -(16 << 56)
const BTRDB_HIGH int64 = (48 << 56)

func helperStatIsNaN(sp *btrdb.StatPoint) bool {
    return math.IsNaN(sp.Min) && math.IsNaN(sp.Mean) && math.IsNaN(sp.Max)
}

/* Tests */

// What happens if you call Nearest on an empty stream?
func TestNearestEmpty(t *testing.T) {
    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    _, _, err := stream.Nearest(ctx, 0, 0, false)
    if err == nil || btrdb.ToCodedError(err).Code != bte.NoSuchPoint {
        t.Fatalf("Expected \"no such point\"; got %v", err)
    }
}

// What if there are no more points to the right?
func TestNearestForwardNoPoint(t *testing.T) {
    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    helperInsert(t, ctx, stream, helperCanonicalData())
    _, _, err := stream.Nearest(ctx, CANONICAL_END + 1, 0, false)
    if err == nil || btrdb.ToCodedError(err).Code != bte.NoSuchPoint {
        t.Fatalf("Expected \"no such point\"; got %v", err)
    }
}

// Check if forward nearest point queries are really inclusive
func TestNearestForwardInclusive(t *testing.T) {
    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    data := helperCanonicalData()
    helperInsert(t, ctx, stream, data)
    rv, _, err := stream.Nearest(ctx, CANONICAL_FINAL, 0, false)
    if err != nil {
        t.Fatalf("Unexpected nearest point error: %v", err)
    }
    if rv != data[len(data) - 1] {
        t.Fatal("Wrong result")
    }
    _, _, err = stream.Nearest(ctx, CANONICAL_FINAL + 1, 0, false)
    if err == nil || btrdb.ToCodedError(err).Code != bte.NoSuchPoint {
        t.Fatalf("Expected \"no such point\"; got %v", err)
    }
}

// What if there are no more points to the left?
func TestNearestBackwardNoPoint(t *testing.T) {
    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    helperInsert(t, ctx, stream, helperCanonicalData())
    _, _, err := stream.Nearest(ctx, CANONICAL_START - 1, 0, true)
    if err == nil || btrdb.ToCodedError(err).Code != bte.NoSuchPoint {
        t.Fatalf("Expected \"no such point\"; got %v", err)
    }
}

// Check if backward nearest point queries are really exclusive
func TestNearestBackwardExclusive(t *testing.T) {
    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    data := helperCanonicalData()
    helperInsert(t, ctx, stream, data)
    _, _, err := stream.Nearest(ctx, CANONICAL_START, 0, true)
    if err == nil || btrdb.ToCodedError(err).Code != bte.NoSuchPoint {
        t.Fatalf("Expected \"no such point\"; got %v", err)
    }
    rv, _, err := stream.Nearest(ctx, CANONICAL_START + 1, 0, true)
    if err != nil {
        t.Fatalf("Unexpected nearest point error: %v", err)
    }
    if rv != data[0] {
        t.Fatal("Wrong result")
    }
}

// Check if the insert range is really inclusive of the earliest time
func TestEarliestInclusive(t *testing.T) {
    t.Skip() // Michael is currently working on this
    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    rp := btrdb.RawPoint{Time: BTRDB_LOW, Value: rand.NormFloat64()}
    helperInsert(t, ctx, stream, []btrdb.RawPoint{rp})
    rv, _, err := stream.Nearest(ctx, BTRDB_LOW, 0, false)
    if err != nil {
        t.Fatalf("Could not find lowest point: %v", err)
    }
    if rv != rp {
        t.Fatal("Lowest point returned incorrectly")
    }
}

// Check if a point before lowest valid time is handled correctly
func TestInsertBeforeRange(t *testing.T) {
    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    err := stream.Insert(ctx, []btrdb.RawPoint{btrdb.RawPoint{Time: BTRDB_LOW - 1, Value: rand.NormFloat64()}})
    if err == nil || btrdb.ToCodedError(err).Code != bte.InvalidTimeRange {
        t.Fatalf("Expected \"invalid time range\" error: got %v", err)
    }
}

// Check if the insert range is really exclusive of the latest time
func TestLatestExclusive(t *testing.T) {
    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    err := stream.Insert(ctx, []btrdb.RawPoint{btrdb.RawPoint{Time: BTRDB_HIGH, Value: rand.NormFloat64()}})
    if err == nil || btrdb.ToCodedError(err).Code != bte.InvalidTimeRange {
        t.Fatalf("Expected \"invalid time range\" error: got %v", err)
    }
}

// Check if a point at the highest valid time is handled correctly
func TestHighestValid(t *testing.T) {
    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    rp := btrdb.RawPoint{Time: BTRDB_HIGH - 1, Value: rand.NormFloat64()}
    helperInsert(t, ctx, stream, []btrdb.RawPoint{rp})
    rv, _, err := stream.Nearest(ctx, BTRDB_HIGH, 0, true)
    if err != nil {
        t.Fatalf("Could not find highest point: %v", err)
    }
    if rv != rp {
        t.Fatal("Highest point returned incorrectly")
    }
}

/* Does the largest possible query work? */
func TestQueryFullTimeRange(t *testing.T) {
    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    data := helperCanonicalData()
    helperInsert(t, ctx, stream, data)
    pts, _ := helperRawQuery(t, ctx, stream, CANONICAL_START, CANONICAL_END, 0)
    if len(data) != len(pts) {
        t.Fatalf("Missing or extra points in queried dataset (inserted %v, got %v)", len(data), len(pts))
    }
    for i, rp := range data {
        if rp != pts[i] {
            t.Fatal("Inserted and queried datasets do not match")
        }
    }
}

/* Check if a query in an invalid time range is handled correctly. */
func TestQueryInvalidTimeRange(t *testing.T) {
    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    data := helperCanonicalData()
    helperInsert(t, ctx, stream, data)
    _, _, errc := stream.RawValues(ctx, CANONICAL_START - 1, CANONICAL_END + 1, 0)
    err := <-errc
    if err == nil || btrdb.ToCodedError(err).Code != bte.InvalidTimeRange {
        t.Fatalf("Expected \"invalid time range\" error: got %v", err)
    }
}

func TestNaN(t *testing.T) {
    nan1 := math.Float64frombits(0x7FFbadc0ffee7ea5)
    nan2 := math.Float64frombits(0x7FF5dbb0554c0010)
    nan3 := math.Float64frombits(0xFFFbabb1edbee71e)
    nan4 := math.Float64frombits(0xFFF501aceca571e5)
    times := []int64{0, 1000, 2000, 3000, 4000, 5000, 6000, 7000}
    values := []float64{nan1, nan2, nan3, rand.NormFloat64(), rand.NormFloat64(), nan4, rand.NormFloat64(), rand.NormFloat64()}

    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    helperInsertTV(t, ctx, stream, times, values)
    pts, _ := helperRawQuery(t, ctx, stream, times[0], times[len(times) - 1] + 1, 0)
    if len(times) != len(pts) {
        t.Fatalf("Missing or extra raw points in queried dataset (expected %v, got %v)", len(times), len(pts))
    }
    for i, rp := range pts {
        if rp.Time != times[i] || math.Float64bits(rp.Value) != math.Float64bits(values[i]) {
            t.Fatal("Inserted and queried datasets do not match")
        }
    }
    spts, _ := helperWindowQuery(t, ctx, stream, 0, 10000, 2000, 0, 0)
    if len(spts) != (len(times) / 2) {
        t.Fatalf("Missing or extra statistical points in queried dataset (expected %v, got %v)", len(times) / 2, len(spts))
    }
    for i, sp := range spts {
        if sp.Time != times[2 * i] {
            t.Fatal("Queried statistical point has unexpected time or count (expected t=%v c=%v, got t=%v c=%v)", times[2 * i], 2, sp.Time, sp.Count)
        }
    }
    if !helperStatIsNaN(&spts[0]) || !helperStatIsNaN(&spts[1]) || !helperStatIsNaN(&spts[2]) {
        t.Fatal("Queried statistical points are not NaN as expected")
    }
    if spts[3].Min != math.Min(values[6], values[7]) || spts[3].Mean != (values[6] + values[7]) / 2 || spts[3].Max != math.Max(values[6], values[7]) {
        t.Fatal("Queried statistical point does not have expected values")
    }
}

func TestInf(t *testing.T) {
    inf1 := math.Inf(1)
    inf2 := math.Inf(-1)
    times := []int64{0, 1000, 2000, 3000, 4000, 5000, 6000, 7000}
    values := []float64{inf1, inf2, inf1, rand.NormFloat64(), rand.NormFloat64(), inf2, rand.NormFloat64(), rand.NormFloat64()}

    ctx := context.Background()
    db := helperConnect(t, ctx)
	stream := helperCreateDefaultStream(t, ctx, db, nil, nil)
    helperInsertTV(t, ctx, stream, times, values)
    pts, _ := helperRawQuery(t, ctx, stream, times[0], times[len(times) - 1] + 1, 0)
    if len(times) != len(pts) {
        t.Fatalf("Missing or extra raw points in queried dataset (expected %v, got %v)", len(times), len(pts))
    }
    for i, rp := range pts {
        if rp.Time != times[i] || math.Float64bits(rp.Value) != math.Float64bits(values[i]) {
            t.Fatal("Inserted and queried datasets do not match")
        }
    }
    spts, _ := helperWindowQuery(t, ctx, stream, 0, 10000, 2000, 0, 0)
    if len(spts) != (len(times) / 2) {
        t.Fatalf("Missing or extra statistical points in queried dataset (expected %v, got %v)", len(times) / 2, len(spts))
    }
    for i, sp := range spts {
        if sp.Time != times[2 * i] {
            t.Fatal("Queried statistical point has unexpected time or count (expected t=%v c=%v, got t=%v c=%v)", times[2 * i], 2, sp.Time, sp.Count)
        }
    }
    if !math.IsInf(spts[0].Min, -1) || !math.IsNaN(spts[0].Mean) || !math.IsInf(spts[0].Max, 1) {
        t.Fatal("Queried statistical point is not (-inf, NaN, +inf) as expected")
    }
    if spts[1].Min != values[3] || !math.IsInf(spts[1].Mean, 1) || !math.IsInf(spts[1].Max, 1) {
        t.Fatalf("Queried statistical point is not (%f, +inf, +inf) as expected", values[3])
    }
    if !math.IsInf(spts[2].Min, -1) || !math.IsInf(spts[2].Mean, -1) || spts[2].Max != values[4] {
        t.Fatalf("Queried statistical point is not (-inf, -inf, %f) as expected", values[4])
    }
    if spts[3].Min != math.Min(values[6], values[7]) || spts[3].Mean != (values[6] + values[7]) / 2 || spts[3].Max != math.Max(values[6], values[7]) {
        t.Fatal("Queried statistical point does not have expected values")
    }
}
